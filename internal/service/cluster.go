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
	"k8s.io/client-go/tools/clientcmd"
)

var ErrForbidden = errors.New("cluster-wide listing is forbidden")

// ClientFactory builds a clientset for a Cluster; dial is nil for direct access. Tests substitute the fake clientset here.
type ClientFactory func(c Cluster, dial DialFunc) (kubernetes.Interface, error)

type DialFunc func(ctx context.Context, network, addr string) (net.Conn, error)

type KubeService struct {
	Namespace string        `json:"namespace"`
	Name      string        `json:"name"`
	Ports     []ServicePort `json:"ports"`
}

type ServicePort struct {
	Name string `json:"name"`
	Port int32  `json:"port"`
}

// newKubeconfigClient uses client-go's standard loader, so exec plugins and OIDC behave as in kubectl.
func newKubeconfigClient(c Cluster, dial DialFunc) (kubernetes.Interface, error) {
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		&clientcmd.ClientConfigLoadingRules{ExplicitPath: c.Kubeconfig},
		&clientcmd.ConfigOverrides{CurrentContext: c.Context},
	).ClientConfig()
	if err != nil {
		return nil, err
	}
	cfg.Timeout = 15 * time.Second
	cfg.Dial = dial
	return kubernetes.NewForConfig(cfg)
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
	client, _, err := s.clusterClient(clusterID)
	if err != nil {
		return "", err
	}
	v, err := client.Discovery().ServerVersionWithContext(ctx)
	if err != nil {
		return "", err
	}
	return v.GitVersion, nil
}

// ListNamespaces returns the Cluster's explicit namespace list, or every namespace when none is set.
func (s *Service) ListNamespaces(ctx context.Context, clusterID string) ([]string, error) {
	client, cluster, err := s.clusterClient(clusterID)
	if err != nil {
		return nil, err
	}
	if len(cluster.Namespaces) > 0 {
		return cluster.Namespaces, nil
	}
	list, err := client.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
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
	client, cluster, err := s.clusterClient(clusterID)
	if err != nil {
		return nil, err
	}
	namespaces := cluster.Namespaces
	if len(namespaces) == 0 {
		namespaces = []string{metav1.NamespaceAll}
	}
	var out []KubeService
	for _, ns := range namespaces {
		list, err := client.CoreV1().Services(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, wrapForbidden(err)
		}
		for _, svc := range list.Items {
			ks := KubeService{Namespace: svc.Namespace, Name: svc.Name}
			for _, p := range svc.Spec.Ports {
				ks.Ports = append(ks.Ports, ServicePort{Name: p.Name, Port: p.Port})
			}
			out = append(out, ks)
		}
	}
	slices.SortFunc(out, func(a, b KubeService) int {
		return cmp.Or(cmp.Compare(a.Namespace, b.Namespace), cmp.Compare(a.Name, b.Name))
	})
	return out, nil
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
func (s *Service) clusterClient(clusterID string) (kubernetes.Interface, Cluster, error) {
	s.mu.Lock()
	cfg, err := loadConfig(s.configPath)
	s.mu.Unlock()
	if err != nil {
		return nil, Cluster{}, err
	}
	i, err := findCluster(cfg, clusterID)
	if err != nil {
		return nil, Cluster{}, err
	}
	cluster := cfg.Clusters[i]
	var dial DialFunc
	if cluster.RouteID != "" {
		dial, err = s.routeDialer(cluster.RouteID)
		if err != nil {
			return nil, Cluster{}, err
		}
	}
	client, err := s.clients(cluster, dial)
	if err != nil {
		return nil, Cluster{}, err
	}
	return client, cluster, nil
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
