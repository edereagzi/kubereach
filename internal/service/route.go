package service

import (
	"cmp"
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"
)

var ErrRouteDown = errors.New("route is not connected")

// CredentialError reports a missing session secret: Code is "passphrase" (Target is the key file)
// or "password" (Target is user@host:port). The UI asks for it and calls ConnectRoute again.
type CredentialError struct {
	Code   string `json:"code"`
	Target string `json:"target"`
}

func (e *CredentialError) Error() string { return e.Code + " required for " + e.Target }

// State is the connection state machine shared by every connection-bearing entity.
type State string

const (
	StateIdle         State = "idle"
	StateConnecting   State = "connecting"
	StateConnected    State = "connected"
	StateReconnecting State = "reconnecting"
	StateStopped      State = "stopped"
	StateError        State = "error"
)

// EventRouteState carries a RouteStatus on every transition.
const EventRouteState = "route:state"

type RouteStatus struct {
	RouteID string `json:"routeId"`
	State   State  `json:"state"`
	Error   string `json:"error,omitempty"`
}

// EventHostKey asks the UI to approve an SSH Server whose host key is not in known_hosts.
const EventHostKey = "route:hostkey"

type HostKeyPrompt struct {
	RouteID     string `json:"routeId"`
	Address     string `json:"address"`
	KeyType     string `json:"keyType"`
	Fingerprint string `json:"fingerprint"`
}

type routeConn struct {
	cancel  context.CancelFunc
	done    chan struct{}
	hostKey chan bool
	mu      sync.Mutex
	clients routeClients
	pending bool
	status  RouteStatus
}

// unknownHostError carries the key the user is asked to approve.
type unknownHostError struct {
	prompt HostKeyPrompt
	key    ssh.PublicKey
}

func (e *unknownHostError) Error() string { return "host key is unknown" }

// fatalError is a failure a retry cannot fix, such as a refused credential or host key; the Route stops in StateError.
type fatalError struct{ error }

func (e fatalError) Unwrap() error { return e.error }

// routeClients holds one client per SSH Server in Route order; the last one reaches the Cluster.
type routeClients []*ssh.Client

func (c routeClients) last() *ssh.Client { return c[len(c)-1] }

func (c routeClients) Close() {
	for i := len(c) - 1; i >= 0; i-- {
		_ = c[i].Close()
	}
}

func (rc *routeConn) sshClient() *ssh.Client {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	if len(rc.clients) == 0 {
		return nil
	}
	return rc.clients.last()
}

func (rc *routeConn) setClients(c routeClients) {
	rc.mu.Lock()
	rc.clients = c
	rc.mu.Unlock()
}

// SaveRoute creates the Route when its ID is empty and replaces it otherwise.
func (s *Service) SaveRoute(r Route) (Route, error) {
	if err := validateRoute(&r); err != nil {
		return Route{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cfg, err := loadConfig(s.configPath)
	if err != nil {
		return Route{}, err
	}
	if r.ID == "" {
		r.ID = newID()
		cfg.Routes = append(cfg.Routes, r)
	} else {
		i, err := findRoute(cfg, r.ID)
		if err != nil {
			return Route{}, err
		}
		cfg.Routes[i] = r
	}
	return r, s.saveConfig(cfg)
}

func validateRoute(r *Route) error {
	if r.Name == "" {
		return errors.New("route name is required")
	}
	if len(r.Servers) == 0 {
		return errors.New("route needs at least one SSH server")
	}
	r.Servers = slices.Clone(r.Servers)
	for i := range r.Servers {
		srv := &r.Servers[i]
		if srv.Port == 0 {
			srv.Port = 22
		}
		switch {
		case srv.Host == "":
			return errors.New("SSH server host is required")
		case srv.User == "":
			return errors.New("SSH server username is required")
		case srv.Port < 1 || srv.Port > 65535:
			return fmt.Errorf("SSH server port %d is out of range", srv.Port)
		case srv.Auth == AuthKeyFile && srv.KeyFile == "":
			return errors.New("key file is required for key authentication")
		case srv.Auth != AuthAgent && srv.Auth != AuthKeyFile && srv.Auth != AuthPassword:
			return fmt.Errorf("unknown authentication method %q", srv.Auth)
		}
	}
	return nil
}

// DeleteRoute refuses while a Cluster still references the Route.
func (s *Service) DeleteRoute(routeID string) error {
	if err := s.StopRoute(routeID); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cfg, err := loadConfig(s.configPath)
	if err != nil {
		return err
	}
	i, err := findRoute(cfg, routeID)
	if err != nil {
		return err
	}
	if slices.ContainsFunc(cfg.Clusters, func(c Cluster) bool { return c.RouteID == routeID }) {
		return fmt.Errorf("route %q is used by a cluster", cfg.Routes[i].Name)
	}
	delete(s.routes, routeID)
	cfg.Routes = slices.Delete(cfg.Routes, i, i+1)
	return s.saveConfig(cfg)
}

// SetClusterRoute attaches a Route to a Cluster; an empty routeID means direct access.
func (s *Service) SetClusterRoute(clusterID, routeID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cfg, err := loadConfig(s.configPath)
	if err != nil {
		return err
	}
	i, err := findCluster(cfg, clusterID)
	if err != nil {
		return err
	}
	if routeID != "" {
		if _, err := findRoute(cfg, routeID); err != nil {
			return err
		}
	}
	cfg.Clusters[i].RouteID = routeID
	return s.saveConfig(cfg)
}

// ConnectRoute starts connecting in the background and returns once credentials are resolved.
// A missing passphrase or password returns a CredentialError; the secret given on the next call fills it and is kept for the session.
func (s *Service) ConnectRoute(routeID, secret string) error {
	s.mu.Lock()
	cfg, err := loadConfig(s.configPath)
	s.mu.Unlock()
	if err != nil {
		return err
	}
	i, err := findRoute(cfg, routeID)
	if err != nil {
		return err
	}
	route := cfg.Routes[i]
	// Resolve credentials once up front so a missing agent, key, passphrase or password is reported synchronously.
	// The given secret fills only the first missing credential, the one the previous call reported.
	for _, srv := range route.Servers {
		_, closer, err := s.authMethod(srv, "")
		var need *CredentialError
		if errors.As(err, &need) && secret != "" {
			_, closer, err = s.authMethod(srv, secret)
			secret = ""
		}
		if err != nil {
			return err
		}
		_ = closer.Close()
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if old := s.routes[routeID]; old != nil {
		select {
		case <-old.done:
		default:
			return nil
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	rc := &routeConn{cancel: cancel, done: make(chan struct{}), hostKey: make(chan bool, 1), status: RouteStatus{RouteID: routeID, State: StateIdle}}
	s.routes[routeID] = rc
	go s.runRoute(ctx, route, rc)
	return nil
}

// StopRoute cancels the connection and any pending retry, returning once the Route is stopped.
func (s *Service) StopRoute(routeID string) error {
	s.mu.Lock()
	rc := s.routes[routeID]
	s.mu.Unlock()
	if rc == nil {
		return nil
	}
	rc.cancel()
	<-rc.done
	return nil
}

// AnswerHostKey resolves the pending EventHostKey for the Route: accept persists the key and continues connecting, reject aborts.
func (s *Service) AnswerHostKey(routeID string, accept bool) error {
	s.mu.Lock()
	rc := s.routes[routeID]
	s.mu.Unlock()
	if rc == nil {
		return fmt.Errorf("route %q is not connecting", routeID)
	}
	rc.mu.Lock()
	defer rc.mu.Unlock()
	if !rc.pending {
		return fmt.Errorf("route %q has no host key waiting for approval", routeID)
	}
	rc.pending = false
	rc.hostKey <- accept
	return nil
}

// RouteStatuses returns the current state of every Route that has been connected this session.
func (s *Service) RouteStatuses() []RouteStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]RouteStatus, 0, len(s.routes))
	for _, rc := range s.routes {
		rc.mu.Lock()
		out = append(out, rc.status)
		rc.mu.Unlock()
	}
	slices.SortFunc(out, func(a, b RouteStatus) int { return cmp.Compare(a.RouteID, b.RouteID) })
	return out
}

// authMethod resolves credentials for one handshake; the closer releases the agent socket afterwards.
// secret fills the first missing passphrase or password and is then remembered for the session.
func (s *Service) authMethod(srv SSHServer, secret string) (ssh.AuthMethod, io.Closer, error) {
	switch srv.Auth {
	case AuthAgent:
		// ponytail: unix socket only; Windows needs the openssh-ssh-agent named pipe.
		conn, err := net.Dial("unix", os.Getenv("SSH_AUTH_SOCK"))
		if err != nil {
			return nil, nil, fmt.Errorf("ssh agent: %w", err)
		}
		return ssh.PublicKeysCallback(agent.NewClient(conn).Signers), conn, nil
	case AuthKeyFile:
		signer, err := s.keySigner(srv.KeyFile, secret)
		if err != nil {
			return nil, nil, err
		}
		return ssh.PublicKeys(signer), io.NopCloser(nil), nil
	case AuthPassword:
		target := passwordTarget(srv)
		password := s.secret(target, secret)
		if password == "" {
			return nil, nil, &CredentialError{Code: "password", Target: target}
		}
		return ssh.Password(password), io.NopCloser(nil), nil
	}
	return nil, nil, fmt.Errorf("unknown authentication method %q", srv.Auth)
}

func passwordTarget(srv SSHServer) string {
	return srv.User + "@" + net.JoinHostPort(srv.Host, strconv.Itoa(srv.Port))
}

// secret returns the remembered secret for target, or remembers and returns the given one.
func (s *Service) secret(target, given string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if given != "" {
		s.secrets[target] = given
	}
	return s.secrets[target]
}

func (s *Service) forgetSecret(target string) {
	s.mu.Lock()
	delete(s.secrets, target)
	s.mu.Unlock()
}

func (s *Service) keySigner(keyFile, passphrase string) (ssh.Signer, error) {
	data, err := os.ReadFile(keyFile)
	if err != nil {
		return nil, err
	}
	signer, err := ssh.ParsePrivateKey(data)
	var missing *ssh.PassphraseMissingError
	if !errors.As(err, &missing) {
		return signer, err
	}
	if passphrase == "" {
		passphrase = s.secret(keyFile, "")
	}
	if passphrase == "" {
		return nil, &CredentialError{Code: "passphrase", Target: keyFile}
	}
	signer, err = ssh.ParsePrivateKeyWithPassphrase(data, []byte(passphrase))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", keyFile, err)
	}
	s.secret(keyFile, passphrase)
	return signer, nil
}

// runRoute keeps the Route up until ctx is cancelled: connect, wait for the drop, back off, repeat.
// The Route is a snapshot; edits apply on the next Connect.
func (s *Service) runRoute(ctx context.Context, route Route, rc *routeConn) {
	defer close(rc.done)
	backoff := time.Second
	s.setRouteState(rc, StateConnecting, nil)
	for {
		clients, err := s.dialRoute(ctx, route)
		if ctx.Err() != nil {
			clients.Close()
			s.setRouteState(rc, StateStopped, nil)
			return
		}
		if err == nil {
			backoff = time.Second
			rc.setClients(clients)
			s.setRouteState(rc, StateConnected, nil)
			closed := make(chan error, 1)
			go func() { closed <- clients.last().Wait() }()
			select {
			case <-ctx.Done():
				rc.setClients(nil)
				clients.Close()
				s.setRouteState(rc, StateStopped, nil)
				return
			case err = <-closed:
			}
			rc.setClients(nil)
			clients.Close()
			if err == nil {
				err = errors.New("connection closed")
			}
		}
		var unknown *unknownHostError
		if errors.As(err, &unknown) {
			rc.mu.Lock()
			rc.pending = true
			rc.mu.Unlock()
			s.Emit(EventHostKey, unknown.prompt)
			select {
			case <-ctx.Done():
				s.setRouteState(rc, StateStopped, nil)
				return
			case accept := <-rc.hostKey:
				if accept {
					err = s.trustHostKey(unknown.prompt.Address, unknown.key)
				} else {
					err = fatalError{fmt.Errorf("%s: host key rejected", unknown.prompt.Address)}
				}
			}
			if err == nil {
				continue
			}
		}
		if errors.As(err, &fatalError{}) {
			s.setRouteState(rc, StateError, err)
			return
		}
		s.setRouteState(rc, StateReconnecting, err)
		select {
		case <-ctx.Done():
			s.setRouteState(rc, StateStopped, nil)
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, 30*time.Second)
	}
}

// dialRoute connects each SSH Server through the previous one, in Route order.
func (s *Service) dialRoute(ctx context.Context, route Route) (routeClients, error) {
	var clients routeClients
	for _, srv := range route.Servers {
		var via *ssh.Client
		if len(clients) > 0 {
			via = clients.last()
		}
		client, err := s.dialServer(ctx, route.ID, via, srv)
		if err != nil {
			clients.Close()
			return nil, err
		}
		clients = append(clients, client)
	}
	return clients, nil
}

// dialServer resolves credentials afresh so a restarted agent is picked up on reconnect,
// and bounds the dial and handshake by ctx so Stop never waits on a stalled server.
// Errors are prefixed with the server address so the UI can name the failed SSH Server.
func (s *Service) dialServer(ctx context.Context, routeID string, via *ssh.Client, srv SSHServer) (client *ssh.Client, err error) {
	addr := net.JoinHostPort(srv.Host, strconv.Itoa(srv.Port))
	defer func() {
		if err != nil {
			err = fmt.Errorf("%s: %w", addr, err)
		}
	}()
	auth, closer, err := s.authMethod(srv, "")
	if err != nil {
		return nil, err
	}
	defer func() { _ = closer.Close() }()
	hostKey, algorithms, err := s.hostKeyCallback(routeID, addr)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var conn net.Conn
	if via == nil {
		conn, err = (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	} else {
		stop := context.AfterFunc(ctx, func() { _ = via.Close() })
		conn, err = via.Dial("tcp", addr)
		if !stop() {
			return nil, ctx.Err()
		}
	}
	if err != nil {
		return nil, err
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	c, chans, reqs, err := ssh.NewClientConn(conn, addr, &ssh.ClientConfig{
		User:              srv.User,
		Auth:              []ssh.AuthMethod{auth},
		HostKeyCallback:   hostKey,
		HostKeyAlgorithms: algorithms,
	})
	if !stop() {
		if err == nil {
			_ = c.Close()
		}
		return nil, ctx.Err()
	}
	if err != nil {
		// A refused password will not fix itself on retry: forget it and stop so the user is asked again.
		if srv.Auth == AuthPassword && isAuthFailure(err) {
			s.forgetSecret(passwordTarget(srv))
			err = fatalError{err}
		}
		return nil, err
	}
	return ssh.NewClient(c, chans, reqs), nil
}

// isAuthFailure matches the handshake error x/crypto/ssh returns when every auth method was refused; it has no sentinel.
func isAuthFailure(err error) bool {
	return strings.Contains(err.Error(), "unable to authenticate")
}

// hostKeyCallback checks against the user's known_hosts; a missing file means every host is unknown.
// An unknown host yields an unknownHostError for the user to decide on; a changed key is fatal.
// The algorithms are those of the keys known_hosts holds for addr, nil when it holds none: left to the client defaults,
// the server may present a key type that is not recorded, and the check would read it as a changed key.
func (s *Service) hostKeyCallback(routeID, addr string) (ssh.HostKeyCallback, []string, error) {
	var files []string
	if _, err := os.Stat(s.KnownHostsPath); err == nil {
		files = append(files, s.KnownHostsPath)
	}
	known, err := knownhosts.New(files...)
	if err != nil {
		return nil, nil, err
	}
	callback := func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		err := known(hostname, remote, key)
		var keyErr *knownhosts.KeyError
		if errors.As(err, &keyErr) && len(keyErr.Want) == 0 {
			return &unknownHostError{
				prompt: HostKeyPrompt{RouteID: routeID, Address: addr, KeyType: key.Type(), Fingerprint: ssh.FingerprintSHA256(key)},
				key:    key,
			}
		}
		if err != nil {
			return fatalError{err}
		}
		return nil
	}
	return callback, knownAlgorithms(known, addr), nil
}

var placeholderHostKey, _ = ssh.NewPublicKey(ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize)).Public())

// knownAlgorithms asks known_hosts about a key it cannot hold; the refusal lists the keys it does hold for addr.
func knownAlgorithms(known ssh.HostKeyCallback, addr string) []string {
	var keyErr *knownhosts.KeyError
	if !errors.As(known(addr, &net.TCPAddr{}, placeholderHostKey), &keyErr) {
		return nil
	}
	var algorithms []string
	for _, k := range keyErr.Want {
		for _, a := range hostKeyAlgorithms(k.Key.Type()) {
			if !slices.Contains(algorithms, a) {
				algorithms = append(algorithms, a)
			}
		}
	}
	return algorithms
}

// hostKeyAlgorithms maps a key type to the signature algorithms that present it; an RSA key signs with several.
func hostKeyAlgorithms(keyType string) []string {
	if keyType == ssh.KeyAlgoRSA {
		return []string{ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256, ssh.KeyAlgoRSA}
	}
	return []string{keyType}
}

// trustHostKey appends the key to known_hosts, creating the file if needed.
func (s *Service) trustHostKey(addr string, key ssh.PublicKey) error {
	if err := os.MkdirAll(filepath.Dir(s.KnownHostsPath), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(s.KnownHostsPath, os.O_APPEND|os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	line := knownhosts.Line([]string{addr}, key) + "\n"
	// An existing file may lack a trailing newline, which would glue the new entry onto the last one.
	if info, err := f.Stat(); err == nil && info.Size() > 0 {
		last := make([]byte, 1)
		if _, err := f.ReadAt(last, info.Size()-1); err == nil && last[0] != '\n' {
			line = "\n" + line
		}
	}
	_, err = f.WriteString(line)
	return err
}

func (s *Service) setRouteState(rc *routeConn, state State, err error) {
	rc.mu.Lock()
	rc.status.State = state
	rc.status.Error = ""
	if err != nil {
		rc.status.Error = err.Error()
	}
	status := rc.status
	rc.mu.Unlock()
	s.Emit(EventRouteState, status)
}

// routeDialer returns a dial function that always uses the Route's live SSH connection.
func (s *Service) routeDialer(routeID string) (DialFunc, error) {
	s.mu.Lock()
	rc := s.routes[routeID]
	s.mu.Unlock()
	if rc == nil || rc.sshClient() == nil {
		return nil, ErrRouteDown
	}
	return func(_ context.Context, network, addr string) (net.Conn, error) {
		client := rc.sshClient()
		if client == nil {
			return nil, ErrRouteDown
		}
		return client.Dial(network, addr)
	}, nil
}

func findRoute(cfg Config, routeID string) (int, error) {
	i := slices.IndexFunc(cfg.Routes, func(r Route) bool { return r.ID == routeID })
	if i < 0 {
		return -1, fmt.Errorf("unknown route %q", routeID)
	}
	return i, nil
}
