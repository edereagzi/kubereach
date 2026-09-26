package service

import (
	"bytes"
	"errors"
	"maps"
	"slices"
	"strings"

	"golang.org/x/crypto/ssh"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// remoteSources are the kubeconfigs Kubereach reads on an SSH Server, by the command that prints each. Only these
// ever run, so a Cluster from an imported configuration cannot run anything else there.
var remoteSources = map[string]string{"~/.kube/config": "cat ~/.kube/config"}

const defaultRemote = "~/.kube/config"

// RemoteContexts reads the kubeconfig on the Route's last SSH Server and lists its contexts; the Route must be connected.
func (s *Service) RemoteContexts(routeID string) ([]string, error) {
	data, err := s.remoteKubeconfig(routeID, defaultRemote)
	if err != nil {
		return nil, err
	}
	kc, err := clientcmd.Load(data)
	if err != nil {
		return nil, &userError{msg: "The kubeconfig on the SSH server is not valid", err: err}
	}
	return slices.Sorted(maps.Keys(kc.Contexts)), nil
}

// ImportRemoteCluster adds a Cluster for one context of the kubeconfig on the Route's last SSH Server, named after that
// server. Only the reference is saved; the kubeconfig is read again on every connect.
func (s *Service) ImportRemoteCluster(routeID, context string) (Cluster, error) {
	data, err := s.remoteKubeconfig(routeID, defaultRemote)
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
	c := Cluster{ID: newID(), Name: route.Servers[len(route.Servers)-1].Host, Remote: defaultRemote, Context: context, RouteID: routeID}
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
	cmd, ok := remoteSources[source]
	if !ok {
		return nil, userErrorf("Kubereach does not read a kubeconfig from %s", source)
	}
	s.mu.Lock()
	rc := s.routes[routeID]
	s.mu.Unlock()
	if rc == nil {
		return nil, ErrRouteDown
	}
	return rc.kubeconfig(source, cmd)
}

// kubeconfig runs without s.mu, and one read at a time so a burst of first requests opens one session.
// A read that finishes after the connection was replaced is returned but not kept.
func (rc *routeConn) kubeconfig(source, cmd string) ([]byte, error) {
	rc.readMu.Lock()
	defer rc.readMu.Unlock()
	client := rc.sshClient()
	if client == nil {
		return nil, ErrRouteDown
	}
	rc.mu.Lock()
	data := rc.kubeconfigs[source]
	rc.mu.Unlock()
	if data != nil {
		return data, nil
	}
	data, err := runRemote(client, cmd)
	if err != nil {
		msg := "Reading " + source + " on the SSH server failed"
		var remote *remoteError
		if errors.As(err, &remote) {
			msg += ": " + remote.stderr
		}
		return nil, &userError{msg: msg, err: err}
	}
	rc.mu.Lock()
	if len(rc.clients) > 0 && rc.clients.last() == client {
		rc.kubeconfigs[source] = data
	}
	rc.mu.Unlock()
	return data, nil
}

// remoteError is a command that failed on the SSH server, with what it printed on stderr.
type remoteError struct {
	stderr string
	err    error
}

func (e *remoteError) Error() string { return e.stderr }
func (e *remoteError) Unwrap() error { return e.err }

// runRemote runs cmd in a session on client and returns what it printed.
func runRemote(client *ssh.Client, cmd string) ([]byte, error) {
	sess, err := client.NewSession()
	if err != nil {
		return nil, err
	}
	defer func() { _ = sess.Close() }()
	var stderr bytes.Buffer
	sess.Stderr = &stderr
	out, err := sess.Output(cmd)
	if msg := strings.TrimSpace(stderr.String()); err != nil && msg != "" {
		return nil, &remoteError{stderr: msg, err: err}
	}
	return out, err
}
