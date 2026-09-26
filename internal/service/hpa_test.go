package service_test

import (
	"context"
	"testing"

	"github.com/edereagzi/kubereach/internal/service"
	"github.com/google/go-cmp/cmp"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func hpa(ns, name, deployment string, lo *int32, hi, current, desired int32, conditions ...autoscalingv2.HorizontalPodAutoscalerCondition) *autoscalingv2.HorizontalPodAutoscaler {
	return &autoscalingv2.HorizontalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		Spec: autoscalingv2.HorizontalPodAutoscalerSpec{
			ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{APIVersion: "apps/v1", Kind: "Deployment", Name: deployment},
			MinReplicas:    lo,
			MaxReplicas:    hi,
		},
		Status: autoscalingv2.HorizontalPodAutoscalerStatus{CurrentReplicas: current, DesiredReplicas: desired, Conditions: conditions},
	}
}

func hpaCondition(kind autoscalingv2.HorizontalPodAutoscalerConditionType, status corev1.ConditionStatus, reason string) autoscalingv2.HorizontalPodAutoscalerCondition {
	return autoscalingv2.HorizontalPodAutoscalerCondition{Type: kind, Status: status, Reason: reason, Message: reason + " message"}
}

func cpuMetric(utilization int32) autoscalingv2.MetricSpec {
	return autoscalingv2.MetricSpec{Type: autoscalingv2.ResourceMetricSourceType, Resource: &autoscalingv2.ResourceMetricSource{
		Name:   corev1.ResourceCPU,
		Target: autoscalingv2.MetricTarget{Type: autoscalingv2.UtilizationMetricType, AverageUtilization: &utilization},
	}}
}

// An autoscaler shows its range, current against desired replicas and each metric's current against its target; one that cannot
// read its metrics or scale its target is a problem, one whose target is scaled to zero is not.
func TestHPAs(t *testing.T) {
	two, current := int32(2), int32(45)
	rps := resource.MustParse("100")
	web := hpa("shop", "web", "web", &two, 10, 3, 5,
		hpaCondition(autoscalingv2.AbleToScale, corev1.ConditionTrue, "SucceededRescale"),
		hpaCondition(autoscalingv2.ScalingActive, corev1.ConditionTrue, "ValidMetricFound"),
		hpaCondition(autoscalingv2.ScalingLimited, corev1.ConditionFalse, "DesiredWithinRange"),
	)
	web.Spec.Metrics = []autoscalingv2.MetricSpec{
		cpuMetric(70),
		{Type: autoscalingv2.PodsMetricSourceType, Pods: &autoscalingv2.PodsMetricSource{
			Metric: autoscalingv2.MetricIdentifier{Name: "requests_per_second"},
			Target: autoscalingv2.MetricTarget{Type: autoscalingv2.AverageValueMetricType, AverageValue: &rps},
		}},
	}
	seen := resource.MustParse("120")
	web.Status.CurrentMetrics = []autoscalingv2.MetricStatus{
		{Type: autoscalingv2.ResourceMetricSourceType, Resource: &autoscalingv2.ResourceMetricStatus{Name: corev1.ResourceCPU, Current: autoscalingv2.MetricValueStatus{AverageUtilization: &current}}},
		{Type: autoscalingv2.PodsMetricSourceType, Pods: &autoscalingv2.PodsMetricStatus{Metric: autoscalingv2.MetricIdentifier{Name: "requests_per_second"}, Current: autoscalingv2.MetricValueStatus{AverageValue: &seen}}},
	}
	noMetrics := hpa("shop", "worker", "worker", nil, 4, 1, 1,
		hpaCondition(autoscalingv2.AbleToScale, corev1.ConditionTrue, "SucceededGetScale"),
		hpaCondition(autoscalingv2.ScalingActive, corev1.ConditionFalse, "FailedGetResourceMetric"),
	)
	noMetrics.Spec.Metrics = []autoscalingv2.MetricSpec{cpuMetric(50)}
	idle := hpa("shop", "idle", "idle", nil, 3, 0, 0, hpaCondition(autoscalingv2.ScalingActive, corev1.ConditionFalse, "ScalingDisabled"))
	ev := event("shop", "web", "Normal", "SuccessfulRescale", "New size: 5; reason: cpu resource utilization (percentage of request) above target", 1, diagEpoch)
	ev.InvolvedObject.Kind = "HorizontalPodAutoscaler"
	svc, cs, id := newFakeService(t, web, noMetrics, idle, ev)
	ctx := context.Background()

	got, err := svc.ListHPAs(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	cond := func(kind, status, reason string) service.HPACondition {
		return service.HPACondition{Type: kind, Status: status, Reason: reason, Message: reason + " message"}
	}
	want := []service.KubeHPA{
		{Namespace: "shop", Name: "idle", TargetKind: "Deployment", TargetName: "idle", Min: 1, Max: 3,
			Conditions: []service.HPACondition{cond("ScalingActive", "False", "ScalingDisabled")}},
		{Namespace: "shop", Name: "web", TargetKind: "Deployment", TargetName: "web", Min: 2, Max: 10, Current: 3, Desired: 5,
			Metrics: []service.HPAMetric{{Name: "cpu", Current: "45%", Target: "70%"}, {Name: "requests_per_second", Current: "120", Target: "100"}},
			Conditions: []service.HPACondition{
				cond("AbleToScale", "True", "SucceededRescale"), cond("ScalingActive", "True", "ValidMetricFound"), cond("ScalingLimited", "False", "DesiredWithinRange"),
			}},
		{Namespace: "shop", Name: "worker", TargetKind: "Deployment", TargetName: "worker", Min: 1, Max: 4, Current: 1, Desired: 1,
			Metrics:    []service.HPAMetric{{Name: "cpu", Target: "50%"}},
			Conditions: []service.HPACondition{cond("AbleToScale", "True", "SucceededGetScale"), cond("ScalingActive", "False", "FailedGetResourceMetric")},
			Problem:    "FailedGetResourceMetric"},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("autoscalers (-want +got):\n%s", diff)
	}

	d, err := svc.DescribeHPA(ctx, id, "shop", "web")
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(want[1], d.HPA); diff != "" {
		t.Errorf("autoscaler (-want +got):\n%s", diff)
	}
	if len(d.Events) != 1 || d.Events[0].Reason != "SuccessfulRescale" {
		t.Errorf("events = %+v; want the SuccessfulRescale one", d.Events)
	}

	forbid(cs, "list", "events", false)
	if d, err = svc.DescribeHPA(ctx, id, "shop", "web"); err != nil || d.Events != nil || d.EventsError == "" {
		t.Errorf("forbidden events = %+v, %v; want none with an error", d, err)
	}
	if _, err := svc.DescribeHPA(ctx, id, "shop", "missing"); err == nil {
		t.Error("missing autoscaler described without error")
	}

	svc, cs, id = newFakeService(t, web)
	forbid(cs, "list", "horizontalpodautoscalers", false)
	if _, err := svc.ListHPAs(ctx, id); err == nil {
		t.Error("forbidden autoscalers listed without error")
	}
}
