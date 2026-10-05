package service

import (
	"bytes"
	"cmp"
	"errors"
	"maps"
	"path"
	"slices"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// remoteSource is a kubeconfig Kubereach knows how to find and read on an SSH Server.
type remoteSource struct {
	name   string // what the Cluster saves and the UI shows
	detect string // holds when the source is on the server
	read   string // prints the kubeconfig; tried plainly, then with sudo -n
	allow  string // what the user can do on the server when both reads are denied
}

// remoteSources are the kubeconfigs Kubereach reads on an SSH Server, the first found winning: a distro's own before
// ~/.kube/config. Only these, and cat of the files the kubeconfig they print names, ever run, so a Cluster from an
// imported configuration cannot run anything else there.
var remoteSources = []remoteSource{
	fileSource("/etc/rancher/k3s/k3s.yaml", writeMode("k3s")),
	fileSource("/etc/rancher/rke2/rke2.yaml", writeMode("rke2")),
	// Absolute paths, as a non-interactive SSH session's PATH and sudo's secure_path can both miss these.
	{name: "k0s kubeconfig admin", detect: "test -e /usr/local/bin/k0s", read: "/usr/local/bin/k0s kubeconfig admin", allow: sudoLine("/usr/local/bin/k0s kubeconfig admin")},
	fileSource("/var/lib/k0s/pki/admin.conf", sudoLine("/usr/bin/cat /var/lib/k0s/pki/admin.conf")),
	{name: "microk8s config -l", detect: "test -e /snap/bin/microk8s", read: "/snap/bin/microk8s config -l", allow: "To allow it, run sudo usermod -a -G microk8s $USER on the server, then reconnect."},
	fileSource("/etc/kubernetes/admin.conf", sudoLine("/usr/bin/cat /etc/kubernetes/admin.conf")),
	fileSource("~/.kube/config", ""),
}

func fileSource(path, allow string) remoteSource {
	return remoteSource{name: path, detect: "test -e " + path, read: "cat " + path, allow: allow}
}

func writeMode(distro string) string {
	return `To allow it, add write-kubeconfig-mode: "0644" to /etc/rancher/` + distro + `/config.yaml on the server and restart ` +
		distro + ` (the same as starting it with --write-kubeconfig-mode 644).`
}

func sudoLine(cmd string) string {
	return `To allow it, run echo "$USER ALL=(root) NOPASSWD: ` + cmd + `" | sudo tee /etc/sudoers.d/kubereach on the server.`
}

// detectKubeconfig prints the name of every source on the server, in one round trip.
var detectKubeconfig = func() string {
	var b strings.Builder
	for _, src := range remoteSources {
		b.WriteString(src.detect + " && echo '" + src.name + "'; ")
	}
	return b.String() + "true"
}()

// RemoteKubeconfig is the kubeconfig found on a Route's last SSH Server and its contexts.
type RemoteKubeconfig struct {
	Source   string   `json:"source"`
	Contexts []string `json:"contexts"`
}

// RemoteContexts finds the kubeconfig on the Route's last SSH Server and lists its contexts; the Route must be connected.
// A source that cannot be read gives way to the next one found, so a copy in ~/.kube/config is used when the distro's
// own is denied; when none can be read, the first one's reason is given.
func (s *Service) RemoteContexts(routeID string) (RemoteKubeconfig, error) {
	rc, err := s.connectedRoute(routeID)
	if err != nil {
		return RemoteKubeconfig{}, err
	}
	client := rc.sshClient()
	if client == nil {
		return RemoteKubeconfig{}, ErrRouteDown
	}
	out, err := s.runRemote(client, detectKubeconfig, "")
	if errors.Is(err, errRemoteTimeout) {
		return RemoteKubeconfig{}, userErrorf("Looking for a kubeconfig on the SSH server did not finish within %s", s.RemoteCommandTimeout)
	}
	if err != nil {
		return RemoteKubeconfig{}, &userError{msg: "Looking for a kubeconfig on the SSH server failed", err: err}
	}
	found := strings.Split(string(out), "\n")
	var first error
	for _, src := range remoteSources {
		if !slices.Contains(found, src.name) {
			continue
		}
		data, err := s.readKubeconfig(rc, src)
		var sudo *CredentialError
		if errors.As(err, &sudo) {
			// The distro's own kubeconfig is worth its sudo password over a copy that may be stale.
			return RemoteKubeconfig{}, err
		}
		if err != nil {
			first = cmp.Or(first, err)
			continue
		}
		kc, err := clientcmd.Load(data)
		if err != nil {
			return RemoteKubeconfig{}, &userError{msg: "The kubeconfig at " + src.name + " on the SSH server is not valid", err: err}
		}
		return RemoteKubeconfig{Source: src.name, Contexts: slices.Sorted(maps.Keys(kc.Contexts))}, nil
	}
	if first != nil {
		return RemoteKubeconfig{}, first
	}
	names := make([]string, len(remoteSources))
	for i, src := range remoteSources {
		names[i] = src.name
	}
	return RemoteKubeconfig{}, userErrorf("The SSH server has no kubeconfig Kubereach knows; it looked for %s", strings.Join(names, ", "))
}

// ImportRemoteCluster adds a Cluster for one context of a kubeconfig on the Route's last SSH Server, named after that
// server. Only the reference is saved; the kubeconfig is read again on every connect.
func (s *Service) ImportRemoteCluster(routeID, source, context string) (Cluster, error) {
	data, err := s.remoteKubeconfig(routeID, source)
	if err != nil {
		return Cluster{}, err
	}
	if _, err := remoteRESTConfig(data, context); err != nil {
		return Cluster{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cfg, err := loadConfig(s.configPath)
	if err != nil {
		return Cluster{}, err
	}
	i, err := findRoute(cfg, routeID)
	if err != nil {
		return Cluster{}, err
	}
	route := cfg.Routes[i]
	c := Cluster{ID: newID(), Name: route.Servers[len(route.Servers)-1].Host, Remote: source, Context: context, RouteID: routeID}
	if j := slices.IndexFunc(cfg.Clusters, func(o Cluster) bool {
		return o.RouteID == c.RouteID && o.Remote == c.Remote && o.Context == c.Context
	}); j >= 0 {
		return Cluster{}, userErrorf("This context is already added as %s", cfg.Clusters[j].Name)
	}
	cfg.Clusters = append(cfg.Clusters, c)
	return c, s.saveConfig(cfg)
}

// remoteRESTConfig refuses, before anything is loaded, a context that would make Kubereach run a local program or read
// a local file, whose content would then go to the server the remote kubeconfig names.
func remoteRESTConfig(data []byte, context string) (*rest.Config, error) {
	kc, err := clientcmd.Load(data)
	if err != nil {
		return nil, &userError{msg: "The kubeconfig on the SSH server is not valid", err: err}
	}
	ctx := kc.Contexts[context]
	if ctx == nil {
		return nil, userErrorf("The kubeconfig on the SSH server has no context %s", context)
	}
	u, c := kc.AuthInfos[ctx.AuthInfo], kc.Clusters[ctx.Cluster]
	if u != nil && (u.Exec != nil || u.AuthProvider != nil) {
		return nil, userErrorf("The kubeconfig on the SSH server signs in with a program, which Kubereach does not run from an SSH server")
	}
	var file string
	if u != nil {
		file = cmp.Or(u.TokenFile, u.ClientCertificate, u.ClientKey)
	}
	if c != nil {
		file = cmp.Or(file, c.CertificateAuthority)
	}
	if file != "" {
		// embedRemoteFiles left it: it could not be read on the server, and it must not be read here.
		return nil, userErrorf("The kubeconfig on the SSH server refers to %s, which Kubereach could not read on the server", file)
	}
	return clientcmd.NewNonInteractiveClientConfig(*kc, context, &clientcmd.ConfigOverrides{}, nil).ClientConfig()
}

// remoteKubeconfig reads the source on the Route's last SSH Server once per connection, so a reconnect reads it afresh.
func (s *Service) remoteKubeconfig(routeID, source string) ([]byte, error) {
	i := slices.IndexFunc(remoteSources, func(src remoteSource) bool { return src.name == source })
	if i < 0 {
		return nil, userErrorf("Kubereach does not read a kubeconfig from %s", source)
	}
	rc, err := s.connectedRoute(routeID)
	if err != nil {
		return nil, err
	}
	return s.readKubeconfig(rc, remoteSources[i])
}

func (s *Service) connectedRoute(routeID string) (*routeConn, error) {
	s.mu.Lock()
	rc := s.routes[routeID]
	s.mu.Unlock()
	if rc == nil {
		return nil, ErrRouteDown
	}
	return rc, nil
}

// kubeconfigRead is a read of a remote kubeconfig in progress; data and err are set before done closes.
type kubeconfigRead struct {
	done chan struct{}
	data []byte
	err  error
}

// readKubeconfig runs without s.mu. Calls that come while src is being read wait for that read and share its result, so
// a burst of first requests opens one session and a read that hangs is waited out once.
// A read that finishes after the connection was replaced is returned but not kept.
func (s *Service) readKubeconfig(rc *routeConn, src remoteSource) ([]byte, error) {
	rc.mu.Lock()
	if len(rc.clients) == 0 {
		rc.mu.Unlock()
		return nil, ErrRouteDown
	}
	// Taken with the read's registration, so a read on a replaced connection is never shared with the new one.
	client := rc.clients.last()
	if data := rc.kubeconfigs[src.name]; data != nil {
		rc.mu.Unlock()
		return data, nil
	}
	if r := rc.reads[src.name]; r != nil {
		rc.mu.Unlock()
		<-r.done
		return r.data, r.err
	}
	r := &kubeconfigRead{done: make(chan struct{})}
	rc.reads[src.name] = r
	rc.mu.Unlock()
	defer close(r.done)

	r.data, r.err = s.runRemote(client, src.read, "")
	if r.err != nil && !errors.Is(r.err, errRemoteTimeout) {
		r.data, r.err = s.sudoRead(client, rc.lastServer, src, r.err)
	}
	if r.err == nil {
		r.data = s.embedRemoteFiles(client, src, r.data)
	}
	if errors.Is(r.err, errRemoteTimeout) {
		r.err = userErrorf("Reading %s on the SSH server did not finish within %s", src.name, s.RemoteCommandTimeout)
	}
	rc.mu.Lock()
	if rc.reads[src.name] == r {
		delete(rc.reads, src.name)
	}
	if r.err == nil && len(rc.clients) > 0 && rc.clients.last() == client {
		rc.kubeconfigs[src.name] = r.data
	}
	rc.mu.Unlock()
	return r.data, r.err
}

// embedRemoteFiles puts the files a kubeconfig names, such as minikube's certificates, into it, read on the same SSH
// Server; a relative path is taken from the kubeconfig's own directory, as kubectl does. A file that cannot be read stays
// a reference, which remoteRESTConfig refuses, so it fails only the contexts that use it.
func (s *Service) embedRemoteFiles(client *ssh.Client, src remoteSource, data []byte) []byte {
	kc, err := clientcmd.Load(data)
	if err != nil {
		return data
	}
	read := map[string][]byte{}
	changed := false
	embed := func(file *string, into *[]byte) {
		if *file == "" {
			return
		}
		p := *file
		if !path.IsAbs(p) && !strings.HasPrefix(p, "~/") {
			p = path.Join(path.Dir(src.name), p)
		}
		b, ok := read[p]
		if !ok {
			b, _ = s.runRemote(client, "cat "+shellPath(p), "")
			read[p] = b
		}
		if len(b) > 0 {
			*into, *file, changed = b, "", true
		}
	}
	for _, u := range kc.AuthInfos {
		embed(&u.ClientCertificate, &u.ClientCertificateData)
		embed(&u.ClientKey, &u.ClientKeyData)
		var token []byte
		if embed(&u.TokenFile, &token); token != nil {
			u.Token = strings.TrimSpace(string(token))
		}
	}
	for _, c := range kc.Clusters {
		embed(&c.CertificateAuthority, &c.CertificateAuthorityData)
	}
	if !changed {
		return data
	}
	out, err := clientcmd.Write(*kc)
	if err != nil {
		return data
	}
	return out
}

// shellPath quotes p for the server's shell, leaving a leading ~/ to expand to the user's home.
func shellPath(p string) string {
	home := ""
	if rest, ok := strings.CutPrefix(p, "~/"); ok {
		home, p = "~/", rest
	}
	return home + "'" + strings.ReplaceAll(p, "'", `'\''`) + "'"
}

// SetSudoPassword keeps the sudo password of the Route's last SSH Server for the session, never on disk; the next read
// that sudo -n cannot do uses it.
func (s *Service) SetSudoPassword(routeID, password string) error {
	rc, err := s.connectedRoute(routeID)
	if err != nil {
		return err
	}
	s.secret(sudoSecret(rc.lastServer), password)
	return nil
}

// sudoSecret is where the sudo password of the SSH Server user@host:port is kept, apart from its SSH password.
func sudoSecret(server string) string { return "sudo " + server }

// sudoRead reads src as root once the plain read failed: with sudo -n, then, when sudo asks for a password, with the
// session's sudo password on stdin. An empty -p prompt keeps it out of the output, and stdin ends after the password so
// a refused one fails instead of waiting for another. LC_ALL=C keeps sudo's messages in the English they are matched
// in; the second of each pair is sudo-rs's wording.
func (s *Service) sudoRead(client *ssh.Client, server string, src remoteSource, plain error) ([]byte, error) {
	data, err := s.runRemote(client, "LC_ALL=C sudo -n "+src.read, "")
	if err == nil || errors.Is(err, errRemoteTimeout) {
		return data, err
	}
	if remoteSays(err, "password is required", "authentication is required") {
		password := s.secret(sudoSecret(server), "")
		if password == "" {
			return nil, &CredentialError{Code: "sudo", Target: server}
		}
		if data, err = s.runRemote(client, "LC_ALL=C sudo -S -p '' "+src.read, password+"\n"); err == nil || errors.Is(err, errRemoteTimeout) {
			return data, err
		}
		if remoteSays(err, "incorrect password", "Authentication failed") {
			s.forgetSecret(sudoSecret(server))
			return nil, &CredentialError{Code: "sudo", Target: server, wrong: true}
		}
		// sudo's own reason now says more than the plain read's, such as the user not being in sudoers.
		plain = err
	}
	// The plain read's reason is the one that says what is wrong; sudo -n's is only that it was refused.
	msg := "Reading " + src.name + " on the SSH server failed"
	var remote *remoteError
	if errors.As(plain, &remote) {
		msg += ": " + remote.stderr
	}
	if src.allow != "" {
		msg += ". " + src.allow
	}
	return nil, &userError{msg: msg, err: plain}
}

// remoteSays tells whether a command failed on the SSH server printing any of texts.
func remoteSays(err error, texts ...string) bool {
	var remote *remoteError
	return errors.As(err, &remote) && slices.ContainsFunc(texts, func(text string) bool { return strings.Contains(remote.stderr, text) })
}

// remoteError is a command that failed on the SSH server, with what it printed on stderr.
type remoteError struct {
	stderr string
	err    error
}

func (e *remoteError) Error() string { return e.stderr }
func (e *remoteError) Unwrap() error { return e.err }

// errRemoteTimeout is a command the SSH server did not finish within RemoteCommandTimeout.
var errRemoteTimeout = errors.New("the command on the SSH server did not finish in time")

// runRemote runs cmd in a session on client with stdin, and returns what it printed. A command that runs past
// RemoteCommandTimeout is sent SIGTERM, which OpenSSH delivers to its process group and sudo passes on, so it does not
// stay on the server with every retry; it is then abandoned, as the session's end may never come while the server is stuck.
func (s *Service) runRemote(client *ssh.Client, cmd, stdin string) ([]byte, error) {
	sess, err := client.NewSession()
	if err != nil {
		return nil, err
	}
	defer func() { _ = sess.Close() }()
	var stderr bytes.Buffer
	sess.Stderr = &stderr
	sess.Stdin = strings.NewReader(stdin)
	type result struct {
		out []byte
		err error
	}
	done := make(chan result, 1)
	go func() {
		out, err := sess.Output(cmd)
		done <- result{out, err}
	}()
	var r result
	select {
	case r = <-done:
	case <-time.After(s.RemoteCommandTimeout):
		_ = sess.Signal(ssh.SIGTERM)
		return nil, errRemoteTimeout
	}
	if msg := strings.TrimSpace(stderr.String()); r.err != nil && msg != "" {
		return nil, &remoteError{stderr: msg, err: r.err}
	}
	return r.out, r.err
}
