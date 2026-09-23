package service

import (
	"cmp"
	"context"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
)

// KubeNode is one node of the Cluster: what it is, what is wrong with it, and how much of it is spoken for.
// Requested and Pods count the pods the scheduler still holds it responsible for.
type KubeNode struct {
	Name    string   `json:"name"`
	Roles   []string `json:"roles,omitempty"`
	Version string   `json:"version"`
	// Problem is the one thing wrong with the node, empty when nothing is; see NodeReason.
	Problem     string        `json:"problem,omitempty"`
	Allocatable ResourceUsage `json:"allocatable"`
	Requested   ResourceUsage `json:"requested"`
	Pods        int           `json:"pods"`
	// PodCapacity is how many pods the node accepts, zero when it does not say.
	PodCapacity int64 `json:"podCapacity"`
	// Unknown marks totals that are a failed read rather than an idle node: the pods could not be listed, so
	// Requested and Pods say nothing and the tab shows no number rather than a wrong one.
	Unknown bool `json:"unknown,omitempty"`
}

type NodeCondition struct {
	Type    string `json:"type"`
	Status  string `json:"status"`
	Reason  string `json:"reason,omitempty"`
	Message string `json:"message,omitempty"`
}

// NodePod is one pod the node is running, with what it uses now and what it reserved.
type NodePod struct {
	Namespace string        `json:"namespace"`
	Name      string        `json:"name"`
	Reason    string        `json:"reason,omitempty"`
	Usage     ResourceUsage `json:"usage"`
	Requests  ResourceUsage `json:"requests"`
}

// NodeDiagnosis answers what is eating the node: its conditions, its pods ranked by what they use, and its events.
type NodeDiagnosis struct {
	Node       KubeNode        `json:"node"`
	Conditions []NodeCondition `json:"conditions"`
	Pods       []NodePod       `json:"pods"`
	// UsageAvailable is false when the Cluster has no metrics.k8s.io; every Usage is then zero because nothing measured it,
	// not because nothing is running, and the pods are in name order rather than ranked.
	UsageAvailable bool `json:"usageAvailable"`
	// PodsError is set when the node's pods could not be listed; the conditions and events still stand.
	PodsError string `json:"podsError,omitempty"`
	// Events is nil and EventsError set when the events could not be listed; the rest of the diagnosis still stands.
	Events      []KubeEvent `json:"events"`
	EventsError string      `json:"eventsError,omitempty"`
}

// ListNodes lists the Cluster's nodes with what each has allocatable and what its pods reserve of it.
func (s *Service) ListNodes(ctx context.Context, clusterID string) ([]KubeNode, error) {
	k, err := s.clusterClient(clusterID)
	if err != nil {
		return nil, err
	}
	nodes, err := cachedNodes(ctx, k)
	if err != nil {
		return nil, err
	}
	// A node carries every namespace's pods, so its totals ignore the Cluster's namespace scope: a total that counted
	// only the scoped namespaces would not be the node's load. A role that may read nodes but not every pod still gets its
	// nodes, with the totals marked unknown; it is the pods that were refused, not the nodes.
	var pods []*corev1.Pod
	var podsErr error
	if len(k.cluster.Namespaces) == 0 {
		pods, podsErr = scopedPods(ctx, k)
	} else {
		pods, podsErr = nodePods(ctx, k)
	}
	out := make([]KubeNode, 0, len(nodes))
	for _, node := range nodes {
		n := nodeObject(node)
		if podsErr != nil {
			n.Unknown = true
		}
		for _, pod := range pods {
			if pod.Spec.NodeName == n.Name && !podIsTerminated(pod) {
				n.holds(pod.Spec)
			}
		}
		out = append(out, n)
	}
	slices.SortFunc(out, func(a, b KubeNode) int { return cmp.Compare(a.Name, b.Name) })
	return out, nil
}

// DescribeNode explains one node: its conditions, the pods it runs ranked by their claim on it, and its events.
func (s *Service) DescribeNode(ctx context.Context, clusterID, name string) (NodeDiagnosis, error) {
	k, m, err := s.metricsClient(clusterID)
	if err != nil {
		return NodeDiagnosis{}, err
	}
	node, err := k.client.CoreV1().Nodes().Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return NodeDiagnosis{}, wrapForbidden(err)
	}
	d := NodeDiagnosis{Node: nodeObject(node)}
	for _, c := range node.Status.Conditions {
		d.Conditions = append(d.Conditions, NodeCondition{Type: string(c.Type), Status: string(c.Status), Reason: c.Reason, Message: c.Message})
	}
	selector := fields.OneTermEqualSelector("spec.nodeName", name).String()
	pods, err := k.client.CoreV1().Pods(metav1.NamespaceAll).List(ctx, metav1.ListOptions{FieldSelector: selector})
	if err != nil {
		d.Node.Unknown, d.PodsError = true, wrapForbidden(err).Error()
		return d, nil
	}
	// Without metrics-server the pods still list; they just cannot be ranked by what they use.
	usage := map[string]ResourceUsage{}
	if list, err := m.PodMetricses(metav1.NamespaceAll).List(ctx, metav1.ListOptions{}); err == nil {
		d.UsageAvailable = true
		for _, pm := range list.Items {
			var u ResourceUsage
			for _, c := range pm.Containers {
				u = u.add(resourceUsage(c.Usage))
			}
			usage[pm.Namespace+"/"+pm.Name] = u
		}
	}
	for i := range pods.Items {
		pod := &pods.Items[i]
		// ponytail: the fake clientset ignores field selectors, so the node is re-checked here; a reactor in the tests would remove this.
		if pod.Spec.NodeName != name || podIsTerminated(pod) {
			continue
		}
		p := NodePod{Namespace: pod.Namespace, Name: pod.Name, Reason: PodReason(pod), Usage: usage[pod.Namespace+"/"+pod.Name]}
		p.Requests = d.Node.holds(pod.Spec)
		d.Pods = append(d.Pods, p)
	}
	rankNodePods(d.Pods, d.Node.Allocatable)
	d.Events, err = objectEvents(ctx, k, "Node", metav1.NamespaceAll, name, string(node.UID))
	if err != nil {
		d.EventsError = err.Error()
	}
	return d, nil
}

func nodeObject(n *corev1.Node) KubeNode {
	return KubeNode{
		Name:        n.Name,
		Roles:       nodeRoles(n.Labels),
		Version:     n.Status.NodeInfo.KubeletVersion,
		Problem:     NodeReason(n),
		Allocatable: resourceUsage(n.Status.Allocatable),
		PodCapacity: n.Status.Allocatable.Pods().Value(),
	}
}

// nodeFaults are the pressures the tab speaks for, worst first, so which one a node badges does not depend on the order
// the API happened to return its conditions in.
var nodeFaults = []corev1.NodeConditionType{corev1.NodeMemoryPressure, corev1.NodeDiskPressure, corev1.NodePIDPressure, corev1.NodeNetworkUnavailable}

// NodeReason is the one word that says what is wrong with a node; it is empty for a node that is Ready and under no pressure.
// A node that does not report Ready at all is not Ready, as in kubectl.
func NodeReason(n *corev1.Node) string {
	status := make(map[corev1.NodeConditionType]corev1.ConditionStatus, len(n.Status.Conditions))
	for _, c := range n.Status.Conditions {
		status[c.Type] = c.Status
	}
	if status[corev1.NodeReady] != corev1.ConditionTrue {
		return "NotReady"
	}
	for _, fault := range nodeFaults {
		if status[fault] == corev1.ConditionTrue {
			return string(fault)
		}
	}
	// A condition this build does not know, from node-problem-detector or a vendor, still reports a fault when it is true.
	for _, c := range n.Status.Conditions {
		if c.Type != corev1.NodeReady && c.Status == corev1.ConditionTrue {
			return string(c.Type)
		}
	}
	return ""
}

const roleLabelPrefix = "node-role.kubernetes.io/"

func nodeRoles(labels map[string]string) []string {
	var roles []string
	for key, value := range labels {
		switch {
		case strings.HasPrefix(key, roleLabelPrefix):
			if role := strings.TrimPrefix(key, roleLabelPrefix); role != "" {
				roles = append(roles, role)
			}
		case key == "kubernetes.io/role" && value != "":
			roles = append(roles, value)
		}
	}
	slices.Sort(roles)
	return slices.Compact(roles)
}

// holds adds a pod the node is still answerable for to its totals, and returns what that pod reserved.
func (n *KubeNode) holds(spec corev1.PodSpec) ResourceUsage {
	requests, _ := podResources(spec)
	n.Requested, n.Pods = n.Requested.add(requests), n.Pods+1
	return requests
}

// podIsTerminated is whether the pod has finished and given the node back, as kubectl describe node counts it.
func podIsTerminated(pod *corev1.Pod) bool {
	return pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed
}

// rankNodePods puts what is eating the node first: each pod by its largest claim on the node's allocatable CPU or memory.
func rankNodePods(pods []NodePod, allocatable ResourceUsage) {
	share := func(p NodePod) float64 {
		var cpu, memory float64
		if allocatable.CPU > 0 {
			cpu = float64(p.Usage.CPU) / float64(allocatable.CPU)
		}
		if allocatable.Memory > 0 {
			memory = float64(p.Usage.Memory) / float64(allocatable.Memory)
		}
		return max(cpu, memory)
	}
	slices.SortFunc(pods, func(a, b NodePod) int {
		return cmp.Or(cmp.Compare(share(b), share(a)), cmp.Compare(a.Namespace, b.Namespace), cmp.Compare(a.Name, b.Name))
	})
}
