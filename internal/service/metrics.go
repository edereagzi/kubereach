package service

import (
	"cmp"
	"context"
	"slices"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	metricsv1beta1 "k8s.io/metrics/pkg/client/clientset/versioned/typed/metrics/v1beta1"
)

// ResourceUsage is CPU in millicores and memory in bytes; zero means none set or none measured.
type ResourceUsage struct {
	CPU    int64 `json:"cpu"`
	Memory int64 `json:"memory"`
}

type PodUsage struct {
	Namespace  string                   `json:"namespace"`
	Name       string                   `json:"name"`
	Usage      ResourceUsage            `json:"usage"`
	Containers map[string]ResourceUsage `json:"containers"`
}

type NodeUsage struct {
	Name  string        `json:"name"`
	Usage ResourceUsage `json:"usage"`
}

// PodMetrics and NodeMetrics are what metrics-server reports; Available is false when the Cluster has no metrics.k8s.io.
type PodMetrics struct {
	Available bool       `json:"available"`
	Pods      []PodUsage `json:"pods"`
}

type NodeMetrics struct {
	Available bool        `json:"available"`
	Nodes     []NodeUsage `json:"nodes"`
}

func (s *Service) PodMetrics(ctx context.Context, clusterID string) (PodMetrics, error) {
	k, m, err := s.metricsClient(clusterID)
	if err != nil {
		return PodMetrics{}, err
	}
	var out PodMetrics
	for _, ns := range k.scope() {
		list, err := m.PodMetricses(ns).List(ctx, metav1.ListOptions{})
		if apierrors.IsNotFound(err) {
			return PodMetrics{}, nil
		}
		if err != nil {
			return PodMetrics{}, wrapForbidden(err)
		}
		for _, pm := range list.Items {
			u := PodUsage{Namespace: pm.Namespace, Name: pm.Name, Containers: make(map[string]ResourceUsage, len(pm.Containers))}
			for _, c := range pm.Containers {
				u.Containers[c.Name] = resourceUsage(c.Usage)
				u.Usage = u.Usage.add(u.Containers[c.Name])
			}
			out.Pods = append(out.Pods, u)
		}
	}
	out.Available = true
	slices.SortFunc(out.Pods, func(a, b PodUsage) int {
		return cmp.Or(cmp.Compare(a.Namespace, b.Namespace), cmp.Compare(a.Name, b.Name))
	})
	return out, nil
}

func (s *Service) NodeMetrics(ctx context.Context, clusterID string) (NodeMetrics, error) {
	_, m, err := s.metricsClient(clusterID)
	if err != nil {
		return NodeMetrics{}, err
	}
	list, err := m.NodeMetricses().List(ctx, metav1.ListOptions{})
	if apierrors.IsNotFound(err) {
		return NodeMetrics{}, nil
	}
	if err != nil {
		return NodeMetrics{}, wrapForbidden(err)
	}
	out := NodeMetrics{Available: true}
	for _, nm := range list.Items {
		out.Nodes = append(out.Nodes, NodeUsage{Name: nm.Name, Usage: resourceUsage(nm.Usage)})
	}
	slices.SortFunc(out.Nodes, func(a, b NodeUsage) int { return cmp.Compare(a.Name, b.Name) })
	return out, nil
}

func (s *Service) metricsClient(clusterID string) (kube, metricsv1beta1.MetricsV1beta1Interface, error) {
	k, err := s.clusterClient(clusterID)
	if err != nil {
		return kube{}, nil, err
	}
	m, err := metricsv1beta1.NewForConfigAndClient(k.config, k.http)
	return k, m, err
}

func (u ResourceUsage) add(o ResourceUsage) ResourceUsage {
	return ResourceUsage{CPU: u.CPU + o.CPU, Memory: u.Memory + o.Memory}
}

func resourceUsage(list corev1.ResourceList) ResourceUsage {
	return ResourceUsage{CPU: list.Cpu().MilliValue(), Memory: list.Memory().Value()}
}

// podResources sums the requests and limits of the containers metrics-server measures: the app containers and the sidecars
// (init containers that keep running). A container without a limit leaves the pod without one.
func podResources(spec corev1.PodSpec) (requests, limits ResourceUsage) {
	cpuOpen, memOpen := false, false
	for _, c := range spec.InitContainers {
		if c.RestartPolicy == nil || *c.RestartPolicy != corev1.ContainerRestartPolicyAlways {
			continue
		}
		spec.Containers = append(spec.Containers, c)
	}
	for _, c := range spec.Containers {
		l := resourceUsage(c.Resources.Limits)
		requests = requests.add(resourceUsage(c.Resources.Requests))
		limits = limits.add(l)
		cpuOpen = cpuOpen || l.CPU == 0
		memOpen = memOpen || l.Memory == 0
	}
	if cpuOpen {
		limits.CPU = 0
	}
	if memOpen {
		limits.Memory = 0
	}
	return requests, limits
}
