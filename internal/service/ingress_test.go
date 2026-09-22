package service_test

import (
	"context"
	"errors"
	"testing"

	"github.com/edereagzi/kubereach/internal/service"
	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func ingressPath(host, path, svc string, port int32) networkingv1.IngressRule {
	return networkingv1.IngressRule{
		Host: host,
		IngressRuleValue: networkingv1.IngressRuleValue{HTTP: &networkingv1.HTTPIngressRuleValue{
			Paths: []networkingv1.HTTPIngressPath{{
				Path:    path,
				Backend: networkingv1.IngressBackend{Service: &networkingv1.IngressServiceBackend{Name: svc, Port: networkingv1.ServiceBackendPort{Number: port}}},
			}},
		}},
	}
}

func endpointSlice(ns, svc string, pods map[string]bool) *discoveryv1.EndpointSlice {
	sl := &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: svc + "-abc", Labels: map[string]string{discoveryv1.LabelServiceName: svc}},
	}
	for name, ready := range pods {
		sl.Endpoints = append(sl.Endpoints, discoveryv1.Endpoint{
			TargetRef:  &corev1.ObjectReference{Kind: "Pod", Namespace: ns, Name: name},
			Conditions: discoveryv1.EndpointConditions{Ready: &ready},
		})
	}
	return sl
}

func TestIngress_RulesResolveThroughServiceToPods(t *testing.T) {
	ing := &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "web"},
		Spec: networkingv1.IngressSpec{
			Rules: []networkingv1.IngressRule{
				ingressPath("api.example.com", "/v1", "api", 8080),
				ingressPath("api.example.com", "/metrics", "api", 8080),
				ingressPath("old.example.com", "/", "gone", 80),
			},
		},
	}
	svc, cs, id := newFakeService(t,
		ing,
		&networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{Namespace: "apps", Name: "bare"}},
		k8sService("default", "api", 8080),
		endpointSlice("default", "api", map[string]bool{"api-0": true, "api-1": false}),
	)
	ctx := context.Background()

	got, err := svc.ListIngresses(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	want := []service.KubeIngress{
		{Namespace: "apps", Name: "bare"},
		{Namespace: "default", Name: "web", Hosts: []string{"api.example.com", "old.example.com"}, Problem: "no Service gone"},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("ingresses (-want +got):\n%s", diff)
	}

	d, err := svc.DescribeIngress(ctx, id, "default", "web")
	if err != nil {
		t.Fatal(err)
	}
	behind := []service.IngressPod{{Namespace: "default", Name: "api-0", Ready: true}, {Namespace: "default", Name: "api-1"}}
	wantChain := service.IngressDiagnosis{
		Ingress: want[1],
		Paths: []service.IngressPath{
			{Host: "api.example.com", Path: "/v1", Service: "api", Port: "8080", Pods: behind},
			{Host: "api.example.com", Path: "/metrics", Service: "api", Port: "8080", Pods: behind},
			{Host: "old.example.com", Path: "/", Service: "gone", Port: "80", Problem: "no Service gone"},
		},
	}
	if diff := cmp.Diff(wantChain, d); diff != "" {
		t.Errorf("chain (-want +got):\n%s", diff)
	}

	// A role that cannot read EndpointSlices sees where the chain stops, not an RBAC message in a badge.
	forbid(cs, "list", "endpointslices", false)
	blocked, err := svc.DescribeIngress(ctx, id, "default", "web")
	if err != nil {
		t.Fatal(err)
	}
	if got := blocked.Paths[0]; got.Problem != "EndpointSlices are forbidden for this role" || !got.Unknown || got.Pods != nil {
		t.Errorf("forbidden endpointslices path = %+v", got)
	}
	// The row still badges the missing Service, and says nothing about the paths it could not read.
	if blocked.Ingress.Problem != "no Service gone" {
		t.Errorf("row problem with endpointslices forbidden = %q; want the missing Service", blocked.Ingress.Problem)
	}

	forbid(cs, "list", "ingresses", false)
	if _, err := svc.ListIngresses(ctx, id); !errors.Is(err, service.ErrForbidden) {
		t.Errorf("forbidden ingresses err = %v; want ErrForbidden", err)
	}
}

func TestDescribeIngress_BackendNamesAPortTheServiceDoesNotExpose(t *testing.T) {
	svc, _, id := newFakeService(t,
		&networkingv1.Ingress{
			ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "web"},
			Spec:       networkingv1.IngressSpec{Rules: []networkingv1.IngressRule{ingressPath("a.example", "/", "api", 9999)}},
		},
		k8sService("default", "api", 8080),
		endpointSlice("default", "api", map[string]bool{"api-0": true}),
	)

	d, err := svc.DescribeIngress(context.Background(), id, "default", "web")
	if err != nil {
		t.Fatal(err)
	}
	want := []service.IngressPath{{
		Host: "a.example", Path: "/", Service: "api", Port: "9999",
		Pods:    []service.IngressPod{{Namespace: "default", Name: "api-0", Ready: true}},
		Problem: "Service api has no port 9999",
	}}
	if diff := cmp.Diff(want, d.Paths); diff != "" {
		t.Errorf("paths (-want +got):\n%s", diff)
	}
}

func TestDescribeIngress_ServiceWithoutReadyEndpoints(t *testing.T) {
	svc, _, id := newFakeService(t,
		&networkingv1.Ingress{
			ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "web"},
			Spec:       networkingv1.IngressSpec{Rules: []networkingv1.IngressRule{ingressPath("a.example", "/", "down", 80), ingressPath("b.example", "/", "empty", 80)}},
		},
		k8sService("default", "down", 80),
		k8sService("default", "empty", 80),
		endpointSlice("default", "down", map[string]bool{"down-0": false}),
	)

	d, err := svc.DescribeIngress(context.Background(), id, "default", "web")
	if err != nil {
		t.Fatal(err)
	}
	want := []service.IngressPath{
		{Host: "a.example", Path: "/", Service: "down", Port: "80", Pods: []service.IngressPod{{Namespace: "default", Name: "down-0"}}, Problem: "no ready endpoints"},
		{Host: "b.example", Path: "/", Service: "empty", Port: "80", Problem: "no endpoints"},
	}
	if diff := cmp.Diff(want, d.Paths); diff != "" {
		t.Errorf("paths (-want +got):\n%s", diff)
	}

	// Two broken paths are one count on the row; the paths themselves are the detail's job.
	listed, err := svc.ListIngresses(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if listed[0].Problem != "2 paths broken" {
		t.Errorf("row problem = %q; want a count of the broken paths", listed[0].Problem)
	}
}
