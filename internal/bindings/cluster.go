package bindings

import (
	"context"
	"os"
	"path/filepath"

	"github.com/edereagzi/kubereach/internal/service"
	"github.com/wailsapp/wails/v3/pkg/application"
)

func init() {
	application.RegisterEvent[service.ClusterChange](service.EventClusterChanged)
}

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
		AttachToWindow(mainWindow()).
		SetDirectory(filepath.Join(home, ".kube")).
		ShowHiddenFiles(true).
		PromptForMultipleSelection()
	if err != nil {
		return nil, err
	}
	return c.svc.ImportKubeconfigs(paths)
}

func (c *ClusterService) ImportPaths(paths []string) ([]service.Cluster, error) {
	return c.svc.ImportKubeconfigs(paths)
}

func (c *ClusterService) Delete(clusterID string) error {
	return c.svc.DeleteCluster(clusterID)
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

func (c *ClusterService) ListPods(ctx context.Context, clusterID string) ([]service.KubePod, error) {
	return c.svc.ListPods(ctx, clusterID)
}

func (c *ClusterService) ListWorkloads(ctx context.Context, clusterID string) ([]service.KubeWorkload, error) {
	return c.svc.ListWorkloads(ctx, clusterID)
}

func (c *ClusterService) SetNamespaces(clusterID string, namespaces []string) error {
	return c.svc.SetNamespaces(clusterID, namespaces)
}

func (c *ClusterService) Rename(clusterID, name string) error {
	return c.svc.RenameCluster(clusterID, name)
}

func (c *ClusterService) DescribePod(ctx context.Context, clusterID, namespace, name string) (service.PodDiagnosis, error) {
	return c.svc.DescribePod(ctx, clusterID, namespace, name)
}

func (c *ClusterService) DescribeWorkload(ctx context.Context, clusterID string, kind service.WorkloadKind, namespace, name string) (service.WorkloadDiagnosis, error) {
	return c.svc.DescribeWorkload(ctx, clusterID, kind, namespace, name)
}

func (c *ClusterService) ListConfigMaps(ctx context.Context, clusterID string) ([]service.KubeConfigObject, error) {
	return c.svc.ListConfigMaps(ctx, clusterID)
}

func (c *ClusterService) ListSecrets(ctx context.Context, clusterID string) ([]service.KubeConfigObject, error) {
	return c.svc.ListSecrets(ctx, clusterID)
}

func (c *ClusterService) GetConfigMap(ctx context.Context, clusterID, namespace, name string) (service.KubeConfigObject, error) {
	return c.svc.GetConfigMap(ctx, clusterID, namespace, name)
}

func (c *ClusterService) GetSecret(ctx context.Context, clusterID, namespace, name string) (service.KubeConfigObject, error) {
	return c.svc.GetSecret(ctx, clusterID, namespace, name)
}

func (c *ClusterService) ListIngresses(ctx context.Context, clusterID string) ([]service.KubeIngress, error) {
	return c.svc.ListIngresses(ctx, clusterID)
}

func (c *ClusterService) DescribeIngress(ctx context.Context, clusterID, namespace, name string) (service.IngressDiagnosis, error) {
	return c.svc.DescribeIngress(ctx, clusterID, namespace, name)
}

func (c *ClusterService) ListPVCs(ctx context.Context, clusterID string) ([]service.KubePVC, error) {
	return c.svc.ListPVCs(ctx, clusterID)
}

func (c *ClusterService) DescribePVC(ctx context.Context, clusterID, namespace, name string) (service.PVCDiagnosis, error) {
	return c.svc.DescribePVC(ctx, clusterID, namespace, name)
}

func (c *ClusterService) ListHPAs(ctx context.Context, clusterID string) ([]service.KubeHPA, error) {
	return c.svc.ListHPAs(ctx, clusterID)
}

func (c *ClusterService) DescribeHPA(ctx context.Context, clusterID, namespace, name string) (service.HPADiagnosis, error) {
	return c.svc.DescribeHPA(ctx, clusterID, namespace, name)
}

func (c *ClusterService) ListNodes(ctx context.Context, clusterID string) ([]service.KubeNode, error) {
	return c.svc.ListNodes(ctx, clusterID)
}

func (c *ClusterService) DescribeNode(ctx context.Context, clusterID, name string) (service.NodeDiagnosis, error) {
	return c.svc.DescribeNode(ctx, clusterID, name)
}

func (c *ClusterService) GetYAML(ctx context.Context, clusterID string, kind service.ObjectKind, namespace, name string, reveal bool) (string, error) {
	return c.svc.GetYAML(ctx, clusterID, kind, namespace, name, reveal)
}

func (c *ClusterService) PodMetrics(ctx context.Context, clusterID string) (service.PodMetrics, error) {
	return c.svc.PodMetrics(ctx, clusterID)
}

func (c *ClusterService) NodeMetrics(ctx context.Context, clusterID string) (service.NodeMetrics, error) {
	return c.svc.NodeMetrics(ctx, clusterID)
}

func (c *ClusterService) RestartWorkload(ctx context.Context, clusterID string, kind service.WorkloadKind, namespace, name string) error {
	return c.svc.RestartWorkload(ctx, clusterID, kind, namespace, name)
}

func (c *ClusterService) DeletePod(ctx context.Context, clusterID, namespace, name string) error {
	return c.svc.DeletePod(ctx, clusterID, namespace, name)
}

func (c *ClusterService) ScaleWorkload(ctx context.Context, clusterID string, kind service.WorkloadKind, namespace, name string, replicas int32) error {
	return c.svc.ScaleWorkload(ctx, clusterID, kind, namespace, name, replicas)
}

func (c *ClusterService) RollbackDeployment(ctx context.Context, clusterID, namespace, name string) error {
	return c.svc.RollbackDeployment(ctx, clusterID, namespace, name)
}
