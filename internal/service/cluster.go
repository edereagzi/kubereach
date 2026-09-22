package service

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"os"
	"reflect"
	"slices"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

var ErrForbidden = errors.New("forbidden")

// ClientFactory builds a clientset and its REST config for a Cluster; dial is nil for direct access.
// Tests substitute the fake clientset here and point the config at an in-test API server.
type ClientFactory func(c Cluster, dial DialFunc) (kubernetes.Interface, *rest.Config, error)

type DialFunc func(ctx context.Context, network, addr string) (net.Conn, error)

type KubeService struct {
	Namespace string      `json:"namespace"`
	Name      string      `json:"name"`
	Ports     []NamedPort `json:"ports"`
}

// NamedPort is a service port or a container port.
type NamedPort struct {
	Name string `json:"name"`
	Port int32  `json:"port"`
}

type KubePod struct {
	Namespace  string      `json:"namespace"`
	Name       string      `json:"name"`
	Containers []string    `json:"containers"`
	Ports      []NamedPort `json:"ports"`
	// Reason is what is wrong with the pod, empty when nothing is; see PodReason.
	Reason   string `json:"reason,omitempty"`
	Restarts int32  `json:"restarts"`
	// LastRestart is when a container last ended before its current run, zero when none has.
	LastRestart time.Time `json:"lastRestart"`
	// Requests and Limits are summed over the measured containers; a zero limit means at least one container has none.
	Requests ResourceUsage `json:"requests"`
	Limits   ResourceUsage `json:"limits"`
}

// kube's clientset and REST config share one HTTP client.
type kube struct {
	client  kubernetes.Interface
	config  *rest.Config
	http    *http.Client
	cluster Cluster
}

// kubeconfigClient uses client-go's standard loader, so exec plugins and OIDC behave as in kubectl.
// Only the clientset speaks protobuf; the returned config stays JSON for metrics and SPDY.
func (s *Service) kubeconfigClient(c Cluster, dial DialFunc) (kubernetes.Interface, *rest.Config, error) {
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		&clientcmd.ClientConfigLoadingRules{ExplicitPath: c.Kubeconfig},
		&clientcmd.ConfigOverrides{CurrentContext: c.Context},
	).ClientConfig()
	if err != nil {
		return nil, nil, err
	}
	cfg.Dial = dial
	cfg.Wrap(func(rt http.RoundTripper) http.RoundTripper { return headerTimeout{rt, s.ResponseHeaderTimeout} })
	proto := rest.CopyConfig(cfg)
	proto.ContentType = "application/vnd.kubernetes.protobuf"
	proto.AcceptContentTypes = "application/vnd.kubernetes.protobuf,application/json"
	client, err := kubernetes.NewForConfig(proto)
	return client, cfg, err
}

// headerTimeout fails a request whose response headers are late, unlike http.Client.Timeout which also cuts a slow body or a watch.
type headerTimeout struct {
	rt http.RoundTripper
	d  time.Duration
}

// noHeaderTimeout marks a request whose headers may legitimately wait, such as a log follow the kubelet answers with its first line.
type noHeaderTimeout struct{}

func (h headerTimeout) RoundTrip(req *http.Request) (*http.Response, error) {
	if h.d <= 0 || req.Context().Value(noHeaderTimeout{}) != nil {
		return h.rt.RoundTrip(req)
	}
	ctx, cancel := context.WithCancel(req.Context())
	timer := time.AfterFunc(h.d, cancel)
	resp, err := h.rt.RoundTrip(req.WithContext(ctx))
	if !timer.Stop() {
		if err == nil {
			_ = resp.Body.Close()
		}
		cancel()
		return nil, fmt.Errorf("%s %s: no response within %v", req.Method, req.URL.Redacted(), h.d)
	}
	if err != nil {
		cancel()
		return nil, err
	}
	resp.Body = cancelOnClose{resp.Body, cancel}
	return resp, nil
}

// WrappedRoundTripper lets client-go find the TLS config and dialer underneath, which the SPDY upgrader needs.
func (h headerTimeout) WrappedRoundTripper() http.RoundTripper { return h.rt }

type cancelOnClose struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (c cancelOnClose) Close() error {
	defer c.cancel()
	return c.ReadCloser.Close()
}

// ImportKubeconfigs adds one Cluster per context not yet configured and returns the added ones.
// Files are referenced by path, never copied. Nothing is saved if any file fails to load.
func (s *Service) ImportKubeconfigs(paths []string) ([]Cluster, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cfg, err := loadConfig(s.configPath)
	if err != nil {
		return nil, err
	}
	var added []Cluster
	for _, path := range paths {
		kc, err := clientcmd.LoadFromFile(path)
		if err != nil {
			return nil, err
		}
		for _, name := range slices.Sorted(maps.Keys(kc.Contexts)) {
			exists := slices.ContainsFunc(cfg.Clusters, func(c Cluster) bool {
				return c.Kubeconfig == path && c.Context == name
			})
			if !exists {
				added = append(added, Cluster{ID: newID(), Name: name, Kubeconfig: path, Context: name})
			}
		}
	}
	if len(added) == 0 {
		return nil, nil
	}
	cfg.Clusters = append(cfg.Clusters, added...)
	return added, s.saveConfig(cfg)
}

// DeleteCluster stops the Cluster's log streams, event streams and shells, drops its client, releases and forgets its Saved Forwards, then forgets the Cluster.
func (s *Service) DeleteCluster(clusterID string) error {
	s.mu.Lock()
	var logs, events, shells []string
	for id, lc := range s.logs {
		lc.mu.Lock()
		if lc.status.Source.ClusterID == clusterID {
			logs = append(logs, id)
		}
		lc.mu.Unlock()
	}
	for id, ec := range s.events {
		if ec.status.ClusterID == clusterID {
			events = append(events, id)
		}
	}
	for id, sc := range s.shells {
		sc.mu.Lock()
		if sc.status.Target.ClusterID == clusterID {
			shells = append(shells, id)
		}
		sc.mu.Unlock()
	}
	s.mu.Unlock()
	for _, id := range logs {
		_ = s.StopLogs(id)
	}
	for _, id := range events {
		_ = s.StopEvents(id)
	}
	for _, id := range shells {
		_ = s.StopShell(id)
	}
	err := s.editForwards(func(cfg *Config) ([]PortForward, error) {
		i, err := findCluster(*cfg, clusterID)
		if err != nil {
			return nil, err
		}
		cfg.Clusters = slices.Delete(cfg.Clusters, i, i+1)
		var gone []PortForward
		cfg.Forwards = slices.DeleteFunc(cfg.Forwards, func(f PortForward) bool {
			if f.ClusterID != clusterID {
				return false
			}
			f.Enabled = false
			gone = append(gone, f)
			return true
		})
		return gone, nil
	})
	if err != nil {
		return err
	}
	s.mu.Lock()
	if k, ok := s.kubes[clusterID]; ok {
		k.release()
		delete(s.kubes, clusterID)
	}
	s.mu.Unlock()
	return nil
}

// CheckReachability returns the API server version, or an error if it cannot be reached.
func (s *Service) CheckReachability(ctx context.Context, clusterID string) (string, error) {
	k, err := s.clusterClient(clusterID)
	if err != nil {
		return "", err
	}
	v, err := k.client.Discovery().ServerVersionWithContext(ctx)
	if err != nil {
		return "", err
	}
	return v.GitVersion, nil
}

// ListNamespaces returns the Cluster's explicit namespace list, or every namespace when none is set.
func (s *Service) ListNamespaces(ctx context.Context, clusterID string) ([]string, error) {
	k, err := s.clusterClient(clusterID)
	if err != nil {
		return nil, err
	}
	if len(k.cluster.Namespaces) > 0 {
		return k.cluster.Namespaces, nil
	}
	list, err := k.client.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, wrapForbidden(err)
	}
	names := make([]string, 0, len(list.Items))
	for _, ns := range list.Items {
		names = append(names, ns.Name)
	}
	slices.Sort(names)
	return names, nil
}

// ListServices lists services in scope: all namespaces, or the Cluster's explicit list.
func (s *Service) ListServices(ctx context.Context, clusterID string) ([]KubeService, error) {
	k, err := s.clusterClient(clusterID)
	if err != nil {
		return nil, err
	}
	var out []KubeService
	for _, ns := range k.scope() {
		list, err := k.client.CoreV1().Services(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, wrapForbidden(err)
		}
		for _, svc := range list.Items {
			ks := KubeService{Namespace: svc.Namespace, Name: svc.Name}
			for _, p := range svc.Spec.Ports {
				ks.Ports = append(ks.Ports, NamedPort{Name: p.Name, Port: p.Port})
			}
			out = append(out, ks)
		}
	}
	slices.SortFunc(out, func(a, b KubeService) int {
		return cmp.Or(cmp.Compare(a.Namespace, b.Namespace), cmp.Compare(a.Name, b.Name))
	})
	return out, nil
}

func kubePod(pod *corev1.Pod) KubePod {
	kp := KubePod{Namespace: pod.Namespace, Name: pod.Name, Containers: containerNames(pod.Spec.Containers), Reason: PodReason(pod), Restarts: podRestarts(pod)}
	kp.Requests, kp.Limits = podResources(pod.Spec)
	for _, c := range pod.Spec.Containers {
		for _, p := range c.Ports {
			kp.Ports = append(kp.Ports, NamedPort{Name: p.Name, Port: p.ContainerPort})
		}
	}
	for _, st := range pod.Status.ContainerStatuses {
		if t := st.LastTerminationState.Terminated; t != nil && t.FinishedAt.After(kp.LastRestart) {
			kp.LastRestart = t.FinishedAt.Time
		}
	}
	return kp
}

// ListPods lists pods in scope with their container ports.
func (s *Service) ListPods(ctx context.Context, clusterID string) ([]KubePod, error) {
	k, err := s.clusterClient(clusterID)
	if err != nil {
		return nil, err
	}
	var out []KubePod
	for _, ns := range k.scope() {
		list, err := k.client.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, wrapForbidden(err)
		}
		for _, pod := range list.Items {
			out = append(out, kubePod(&pod))
		}
	}
	slices.SortFunc(out, func(a, b KubePod) int {
		return cmp.Or(cmp.Compare(a.Namespace, b.Namespace), cmp.Compare(a.Name, b.Name))
	})
	return out, nil
}

// scope is the namespaces to list: the Cluster's explicit list, or all.
func (k kube) scope() []string {
	if len(k.cluster.Namespaces) > 0 {
		return k.cluster.Namespaces
	}
	return []string{metav1.NamespaceAll}
}

// SetNamespaces stores an explicit namespace scope on the Cluster; empty means all namespaces.
func (s *Service) SetNamespaces(clusterID string, namespaces []string) error {
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
	cfg.Clusters[i].Namespaces = namespaces
	return s.saveConfig(cfg)
}

// clusterClient reuses the Cluster's client until its definition or kubeconfig file changes.
// A Route's live connection is resolved at dial time, so a restarted Route needs no rebuild.
func (s *Service) clusterClient(clusterID string) (kube, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cfg, err := loadConfig(s.configPath)
	if err != nil {
		return kube{}, err
	}
	i, err := findCluster(cfg, clusterID)
	if err != nil {
		return kube{}, err
	}
	c := cfg.Clusters[i]
	var dial DialFunc
	if c.RouteID != "" {
		if s.routeSSH(c.RouteID) == nil {
			return kube{}, ErrRouteDown
		}
		dial = s.routeDial(c.RouteID)
	}
	var stamp kubeconfigStamp
	if fi, err := os.Stat(c.Kubeconfig); err == nil {
		stamp = kubeconfigStamp{fi.ModTime().UnixNano(), fi.Size()}
	}
	if old, ok := s.kubes[clusterID]; ok {
		if old.stamp == stamp && reflect.DeepEqual(old.cluster, c) {
			return old.kube, nil
		}
		old.release()
		delete(s.kubes, clusterID)
	}
	k := kube{cluster: c}
	if k.client, k.config, err = s.clients(c, dial); err != nil {
		return kube{}, err
	}
	if k.http, err = sharedHTTPClient(k.client, k.config); err != nil {
		return kube{}, err
	}
	s.kubes[clusterID] = cachedKube{kube: k, stamp: stamp}
	return k, nil
}

type kubeconfigStamp struct{ mod, size int64 }

type cachedKube struct {
	kube
	stamp kubeconfigStamp
}

// sharedHTTPClient is the transport the clientset already holds, so every other client of the Cluster reuses its connections.
func sharedHTTPClient(client kubernetes.Interface, config *rest.Config) (*http.Client, error) {
	if cs, ok := client.(*kubernetes.Clientset); ok {
		if rc, ok := cs.CoreV1().RESTClient().(*rest.RESTClient); ok && rc.Client != nil {
			return rc.Client, nil
		}
	}
	if config == nil {
		return nil, nil
	}
	return rest.HTTPClientFor(config)
}

func (k kube) release() {
	if k.http != nil {
		k.http.CloseIdleConnections()
	}
}

func findCluster(cfg Config, clusterID string) (int, error) {
	i := slices.IndexFunc(cfg.Clusters, func(c Cluster) bool { return c.ID == clusterID })
	if i < 0 {
		return -1, fmt.Errorf("unknown cluster %q", clusterID)
	}
	return i, nil
}

func wrapForbidden(err error) error {
	if apierrors.IsForbidden(err) {
		return fmt.Errorf("%w: %w", ErrForbidden, err)
	}
	return err
}

func newID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
