package service

import (
	"bytes"
	"cmp"
	"errors"
	"maps"
	"slices"
	"strings"

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
// ~/.kube/config. Only these ever run, so a Cluster from an imported configuration cannot run anything else there.
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
	out, err := runRemote(client, detectKubeconfig, "")
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
	if u != nil && (u.Exec != nil || u.AuthProvider != nil || u.TokenFile != "" || u.ClientCertificate != "" || u.ClientKey != "") ||
		c != nil && c.CertificateAuthority != "" {
		return nil, userErrorf("The kubeconfig on the SSH server refers to local files or programs, which Kubereach does not use from an SSH server")
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

// readKubeconfig runs without s.mu, and one read at a time so a burst of first requests opens one session.
// A read that finishes after the connection was replaced is returned but not kept.
func (s *Service) readKubeconfig(rc *routeConn, src remoteSource) ([]byte, error) {
	rc.readMu.Lock()
	defer rc.readMu.Unlock()
	client := rc.sshClient()
	if client == nil {
		return nil, ErrRouteDown
	}
	rc.mu.Lock()
	data := rc.kubeconfigs[src.name]
	rc.mu.Unlock()
	if data != nil {
		return data, nil
	}
	data, err := runRemote(client, src.read, "")
	if err != nil {
		if data, err = s.sudoRead(client, rc.lastServer, src, err); err != nil {
			return nil, err
		}
	}
	rc.mu.Lock()
	if len(rc.clients) > 0 && rc.clients.last() == client {
		rc.kubeconfigs[src.name] = data
	}
	rc.mu.Unlock()
	return data, nil
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
	data, err := runRemote(client, "LC_ALL=C sudo -n "+src.read, "")
	if err == nil {
		return data, nil
	}
	if remoteSays(err, "password is required", "authentication is required") {
		password := s.secret(sudoSecret(server), "")
		if password == "" {
			return nil, &CredentialError{Code: "sudo", Target: server}
		}
		if data, err = runRemote(client, "LC_ALL=C sudo -S -p '' "+src.read, password+"\n"); err == nil {
			return data, nil
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

// runRemote runs cmd in a session on client with stdin, and returns what it printed.
func runRemote(client *ssh.Client, cmd, stdin string) ([]byte, error) {
	sess, err := client.NewSession()
	if err != nil {
		return nil, err
	}
	defer func() { _ = sess.Close() }()
	var stderr bytes.Buffer
	sess.Stderr = &stderr
	sess.Stdin = strings.NewReader(stdin)
	out, err := sess.Output(cmd)
	if msg := strings.TrimSpace(stderr.String()); err != nil && msg != "" {
		return nil, &remoteError{stderr: msg, err: err}
	}
	return out, err
}
