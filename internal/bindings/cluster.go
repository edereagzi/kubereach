package bindings

import (
	"context"
	"os"
	"path/filepath"

	"github.com/edereagzi/kubereach/internal/service"
	"github.com/wailsapp/wails/v3/pkg/application"
)

type ClusterService struct {
	svc *service.Service
}

func NewClusterService(svc *service.Service) *ClusterService {
	return &ClusterService{svc: svc}
}

// Import opens the native file picker and imports every chosen kubeconfig.
func (c *ClusterService) Import() ([]service.Cluster, error) {
	home, _ := os.UserHomeDir()
	paths, err := application.Get().Dialog.OpenFile().
		SetTitle("Import kubeconfig").
		SetDirectory(filepath.Join(home, ".kube")).
		ShowHiddenFiles(true).
		PromptForMultipleSelection()
	if err != nil {
		return nil, err
	}
	return c.svc.ImportKubeconfigs(paths)
}

func (c *ClusterService) CheckReachability(ctx context.Context, clusterID string) (string, error) {
	return c.svc.CheckReachability(ctx, clusterID)
}

func (c *ClusterService) ListNamespaces(ctx context.Context, clusterID string) ([]string, error) {
	return c.svc.ListNamespaces(ctx, clusterID)
}

func (c *ClusterService) ListServices(ctx context.Context, clusterID string) ([]service.KubeService, error) {
	return c.svc.ListServices(ctx, clusterID)
}

func (c *ClusterService) SetNamespaces(clusterID string, namespaces []string) error {
	return c.svc.SetNamespaces(clusterID, namespaces)
}
