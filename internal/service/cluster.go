package service

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"net"
	"slices"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

var ErrForbidden = errors.New("cluster-wide listing is forbidden")

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
	Namespace string      `json:"namespace"`
	Name      string      `json:"name"`
	Ports     []NamedPort `json:"ports"`
}

// kube is one Cluster's clientset and REST config, built per call so dialing always uses the Route's live connection.
type kube struct {
	client  kubernetes.Interface
	config  *rest.Config
	cluster Cluster
}

// newKubeconfigClient uses client-go's standard loader, so exec plugins and OIDC behave as in kubectl.
func newKubeconfigClient(c Cluster, dial DialFunc) (kubernetes.Interface, *rest.Config, error) {
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		&clientcmd.ClientConfigLoadingRules{ExplicitPath: c.Kubeconfig},
		&clientcmd.ConfigOverrides{CurrentContext: c.Context},
	).ClientConfig()
	if err != nil {
		return nil, nil, err
	}
	cfg.Timeout = 15 * time.Second
	cfg.Dial = dial
	client, err := kubernetes.NewForConfig(cfg)
	return client, cfg, err
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
	return added, saveConfig(s.configPath, cfg)
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
			kp := KubePod{Namespace: pod.Namespace, Name: pod.Name}
			for _, c := range pod.Spec.Containers {
				for _, p := range c.Ports {
					kp.Ports = append(kp.Ports, NamedPort{Name: p.Name, Port: p.ContainerPort})
				}
			}
			out = append(out, kp)
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
	return saveConfig(s.configPath, cfg)
}

// ponytail: builds a fresh clientset per call; the dial function always reaches the Route's live SSH connection.
func (s *Service) clusterClient(clusterID string) (kube, error) {
	s.mu.Lock()
	cfg, err := loadConfig(s.configPath)
	s.mu.Unlock()
	if err != nil {
		return kube{}, err
	}
	i, err := findCluster(cfg, clusterID)
	if err != nil {
		return kube{}, err
	}
	k := kube{cluster: cfg.Clusters[i]}
	var dial DialFunc
	if k.cluster.RouteID != "" {
		dial, err = s.routeDialer(k.cluster.RouteID)
		if err != nil {
			return kube{}, err
		}
	}
	k.client, k.config, err = s.clients(k.cluster, dial)
	if err != nil {
		return kube{}, err
	}
	return k, nil
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
