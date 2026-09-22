package service

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"slices"
	"strconv"

	discoveryv1 "k8s.io/api/discovery/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// KubeIngress is an Ingress in scope. Hosts is every rule host once, so a row can show the first and count the rest;
// Problem is what is wrong with the chain behind it, which is what a row badges. The paths themselves are in DescribeIngress.
type KubeIngress struct {
	Namespace string   `json:"namespace"`
	Name      string   `json:"name"`
	Hosts     []string `json:"hosts,omitempty"`
	Problem   string   `json:"problem,omitempty"`
}

// IngressPath is one rule of an Ingress followed to the pods that serve it. Host and Path are empty for the default backend.
type IngressPath struct {
	Host    string `json:"host,omitempty"`
	Path    string `json:"path,omitempty"`
	Service string `json:"service,omitempty"`
	// Port is the backend port by name or number, as the Ingress names it.
	Port string       `json:"port,omitempty"`
	Pods []IngressPod `json:"pods,omitempty"`
	// Problem says where the chain stops: no such Service, a port it does not expose, nothing behind it, or nothing ready.
	Problem string `json:"problem,omitempty"`
	// Unknown marks a stop that is a failed read rather than a broken chain, so it does not read as a fault.
	Unknown bool `json:"unknown,omitempty"`
}

// IngressPod is one pod an EndpointSlice points at.
type IngressPod struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Ready     bool   `json:"ready"`
}

type IngressDiagnosis struct {
	Ingress KubeIngress   `json:"ingress"`
	Paths   []IngressPath `json:"paths"`
}

// ListIngresses lists Ingresses in scope, each already followed to its pods so a row can say what is wrong with it.
// The two bulk reads that costs are small beside the pods and config objects the same screen already lists.
func (s *Service) ListIngresses(ctx context.Context, clusterID string) ([]KubeIngress, error) {
	k, err := s.clusterClient(clusterID)
	if err != nil {
		return nil, err
	}
	behind := newBackends(k)
	var out []KubeIngress
	for _, ns := range k.scope() {
		list, err := k.client.NetworkingV1().Ingresses(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, wrapForbidden(err)
		}
		for i := range list.Items {
			o := ingressObject(&list.Items[i])
			o.Problem = worstProblem(behind.follow(ctx, &list.Items[i]))
			out = append(out, o)
		}
	}
	slices.SortFunc(out, func(a, b KubeIngress) int {
		return cmp.Or(cmp.Compare(a.Namespace, b.Namespace), cmp.Compare(a.Name, b.Name))
	})
	return out, nil
}

// DescribeIngress follows every path of an Ingress to its Service, the Service's EndpointSlices and the pods behind them,
// so one view answers which URL hits which pod.
func (s *Service) DescribeIngress(ctx context.Context, clusterID, namespace, name string) (IngressDiagnosis, error) {
	k, err := s.clusterClient(clusterID)
	if err != nil {
		return IngressDiagnosis{}, err
	}
	ing, err := k.client.NetworkingV1().Ingresses(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return IngressDiagnosis{}, wrapForbidden(err)
	}
	d := IngressDiagnosis{Ingress: ingressObject(ing), Paths: newBackends(k).follow(ctx, ing)}
	d.Ingress.Problem = worstProblem(d.Paths)
	return d, nil
}

func ingressObject(ing *networkingv1.Ingress) KubeIngress {
	o := KubeIngress{Namespace: ing.Namespace, Name: ing.Name}
	for _, r := range ing.Spec.Rules {
		if r.Host != "" && !slices.Contains(o.Hosts, r.Host) {
			o.Hosts = append(o.Hosts, r.Host)
		}
	}
	return o
}

// worstProblem is the one thing a row says about the chain: the single fault when there is one, a count when there are more.
// A path whose read failed is not a fault and is left to the detail.
func worstProblem(paths []IngressPath) string {
	var first string
	n := 0
	for _, p := range paths {
		if p.Problem == "" || p.Unknown {
			continue
		}
		n++
		if first == "" {
			first = p.Problem
		}
	}
	if n > 1 {
		return fmt.Sprintf("%d paths broken", n)
	}
	return first
}

// backends answers what is behind a Service, reading a namespace's Services and EndpointSlices once for every path that
// asks. Two bulk lists per namespace cost less than the get and list each backend would need on its own.
type backends struct {
	k      kube
	loaded map[string]*namespaceBackends
}

type namespaceBackends struct {
	ports  map[string][]NamedPort
	pods   map[string][]IngressPod
	svcErr error
	epErr  error
}

// serviceBackend is one Service's pods, or why the chain stops at it.
type serviceBackend struct {
	found   bool
	ports   []NamedPort
	pods    []IngressPod
	problem string
	unknown bool
}

func newBackends(k kube) *backends {
	return &backends{k: k, loaded: map[string]*namespaceBackends{}}
}

func (b *backends) follow(ctx context.Context, ing *networkingv1.Ingress) []IngressPath {
	var out []IngressPath
	for _, r := range ing.Spec.Rules {
		if r.HTTP == nil {
			continue
		}
		for _, p := range r.HTTP.Paths {
			out = append(out, b.path(ctx, ing.Namespace, r.Host, p.Path, p.Backend))
		}
	}
	if ing.Spec.DefaultBackend != nil {
		out = append(out, b.path(ctx, ing.Namespace, "", "", *ing.Spec.DefaultBackend))
	}
	return out
}

func (b *backends) path(ctx context.Context, namespace, host, path string, backend networkingv1.IngressBackend) IngressPath {
	p := IngressPath{Host: host, Path: path}
	if backend.Service == nil {
		p.Problem = "backend is not a Service"
		return p
	}
	p.Service, p.Port = backend.Service.Name, backendPort(backend.Service.Port)
	got := b.service(ctx, namespace, p.Service)
	p.Pods, p.Problem, p.Unknown = got.pods, got.problem, got.unknown
	// A port the Service does not expose is a definite fault, so it outranks endpoints that could not be read.
	if got.found && (p.Problem == "" || p.Unknown) && !servesPort(got.ports, backend.Service.Port) {
		p.Problem, p.Unknown = fmt.Sprintf("Service %s has no port %s", p.Service, p.Port), false
	}
	return p
}

func (b *backends) service(ctx context.Context, namespace, name string) serviceBackend {
	ns, ok := b.loaded[namespace]
	if !ok {
		ns = b.load(ctx, namespace)
		b.loaded[namespace] = ns
	}
	if ns.svcErr != nil {
		return stoppedAt("Services", ns.svcErr)
	}
	ports, found := ns.ports[name]
	if !found {
		return serviceBackend{problem: fmt.Sprintf("no Service %s", name)}
	}
	got := serviceBackend{found: true, ports: ports}
	if ns.epErr != nil {
		got.problem, got.unknown = stoppedAt("EndpointSlices", ns.epErr).problem, true
		return got
	}
	got.pods = ns.pods[name]
	switch {
	case len(got.pods) == 0:
		got.problem = "no endpoints"
	case !slices.ContainsFunc(got.pods, func(p IngressPod) bool { return p.Ready }):
		got.problem = "no ready endpoints"
	}
	return got
}

func (b *backends) load(ctx context.Context, namespace string) *namespaceBackends {
	out := &namespaceBackends{ports: map[string][]NamedPort{}, pods: map[string][]IngressPod{}}
	svcs, err := b.k.client.CoreV1().Services(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		out.svcErr = err
		return out
	}
	for _, svc := range svcs.Items {
		ports := make([]NamedPort, 0, len(svc.Spec.Ports))
		for _, p := range svc.Spec.Ports {
			ports = append(ports, NamedPort{Name: p.Name, Port: p.Port})
		}
		out.ports[svc.Name] = ports
	}
	list, err := b.k.client.DiscoveryV1().EndpointSlices(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		out.epErr = err
		return out
	}
	// A pod appears once per address family, so slices of one Service are merged by name.
	byService := map[string]map[string]IngressPod{}
	for _, sl := range list.Items {
		svc := sl.Labels[discoveryv1.LabelServiceName]
		if svc == "" {
			continue
		}
		for _, e := range sl.Endpoints {
			if e.TargetRef == nil || e.TargetRef.Kind != "Pod" {
				continue
			}
			pod := IngressPod{Namespace: cmp.Or(e.TargetRef.Namespace, sl.Namespace), Name: e.TargetRef.Name, Ready: e.Conditions.Ready == nil || *e.Conditions.Ready}
			if byService[svc] == nil {
				byService[svc] = map[string]IngressPod{}
			}
			if was, seen := byService[svc][pod.Name]; !seen || was.Ready {
				byService[svc][pod.Name] = pod
			}
		}
	}
	for svc, pods := range byService {
		for _, n := range slices.Sorted(maps.Keys(pods)) {
			out.pods[svc] = append(out.pods[svc], pods[n])
		}
	}
	return out
}

// servesPort is whether the Service exposes the port the Ingress names, by name or by number.
func servesPort(ports []NamedPort, want networkingv1.ServiceBackendPort) bool {
	return slices.ContainsFunc(ports, func(p NamedPort) bool {
		if want.Name != "" {
			return p.Name == want.Name
		}
		return p.Port == want.Number
	})
}

// stoppedAt says the chain could not be followed, not that it is broken; a forbidden read is shortened to fit beside a path.
func stoppedAt(what string, err error) serviceBackend {
	if apierrors.IsForbidden(err) {
		return serviceBackend{problem: what + " are forbidden for this role", unknown: true}
	}
	return serviceBackend{problem: fmt.Sprintf("%s could not be read: %v", what, err), unknown: true}
}

func backendPort(p networkingv1.ServiceBackendPort) string {
	if p.Name != "" {
		return p.Name
	}
	return strconv.Itoa(int(p.Number))
}
