package service

import (
	"cmp"
	"context"
	"fmt"
	"slices"

	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// KubeHPA is a HorizontalPodAutoscaler in scope: the workload it scales, its range, and what it read and decided.
type KubeHPA struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	// TargetKind is the Kubernetes kind of the scaled workload, such as Deployment.
	TargetKind string         `json:"targetKind"`
	TargetName string         `json:"targetName"`
	Min        int32          `json:"min"`
	Max        int32          `json:"max"`
	Current    int32          `json:"current"`
	Desired    int32          `json:"desired"`
	Metrics    []HPAMetric    `json:"metrics"`
	Conditions []HPACondition `json:"conditions"`
	// Problem is the reason it cannot scale its target or read its metrics; a target scaled to zero is not one.
	Problem string `json:"problem,omitempty"`
}

// HPAMetric is one metric the autoscaler scales on, as kubectl prints it: "45%" against "70%". Current is empty while it cannot be read.
type HPAMetric struct {
	Name    string `json:"name"`
	Current string `json:"current,omitempty"`
	Target  string `json:"target"`
}

// HPACondition is one of its status conditions; ScalingActive and AbleToScale say whether it can act, ScalingLimited why it stopped at its range.
type HPACondition struct {
	Type    string `json:"type"`
	Status  string `json:"status"`
	Reason  string `json:"reason"`
	Message string `json:"message"`
}

// HPADiagnosis is an autoscaler with its events, which say when and why it rescaled.
type HPADiagnosis struct {
	HPA KubeHPA `json:"hpa"`
	// Events is nil and EventsError set when the events could not be read.
	Events      []KubeEvent `json:"events"`
	EventsError string      `json:"eventsError,omitempty"`
}

// ListHPAs lists HorizontalPodAutoscalers in scope.
func (s *Service) ListHPAs(ctx context.Context, clusterID string) ([]KubeHPA, error) {
	k, err := s.clusterClient(clusterID)
	if err != nil {
		return nil, err
	}
	hpas, err := cached(ctx, k, "hpas", []string{"hpas"}, k.scope(), &autoscalingv2.HorizontalPodAutoscaler{}, func(ns string) listWatcher[*autoscalingv2.HorizontalPodAutoscalerList] {
		return k.client.AutoscalingV2().HorizontalPodAutoscalers(ns)
	})
	if err != nil {
		return nil, err
	}
	out := make([]KubeHPA, 0, len(hpas))
	for _, h := range hpas {
		out = append(out, kubeHPA(h))
	}
	slices.SortFunc(out, func(a, b KubeHPA) int {
		return cmp.Or(cmp.Compare(a.Namespace, b.Namespace), cmp.Compare(a.Name, b.Name))
	})
	return out, nil
}

// DescribeHPA is one autoscaler and its events.
func (s *Service) DescribeHPA(ctx context.Context, clusterID, namespace, name string) (HPADiagnosis, error) {
	k, err := s.clusterClient(clusterID)
	if err != nil {
		return HPADiagnosis{}, err
	}
	h, err := k.client.AutoscalingV2().HorizontalPodAutoscalers(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return HPADiagnosis{}, wrapForbidden(err)
	}
	d := HPADiagnosis{HPA: kubeHPA(h)}
	d.Events, err = objectEvents(ctx, k, "HorizontalPodAutoscaler", namespace, name, string(h.UID))
	if err != nil {
		d.EventsError = errorMessage(err)
	}
	return d, nil
}

func kubeHPA(h *autoscalingv2.HorizontalPodAutoscaler) KubeHPA {
	o := KubeHPA{
		Namespace: h.Namespace, Name: h.Name,
		TargetKind: h.Spec.ScaleTargetRef.Kind, TargetName: h.Spec.ScaleTargetRef.Name,
		Min: 1, Max: h.Spec.MaxReplicas,
		Current: h.Status.CurrentReplicas, Desired: h.Status.DesiredReplicas,
	}
	if h.Spec.MinReplicas != nil {
		o.Min = *h.Spec.MinReplicas
	}
	// Like kubectl, a metric's status is the one at its index.
	for i, m := range h.Spec.Metrics {
		var st autoscalingv2.MetricStatus
		if i < len(h.Status.CurrentMetrics) {
			st = h.Status.CurrentMetrics[i]
		}
		o.Metrics = append(o.Metrics, hpaMetric(m, st))
	}
	for _, c := range h.Status.Conditions {
		o.Conditions = append(o.Conditions, HPACondition{Type: string(c.Type), Status: string(c.Status), Reason: c.Reason, Message: c.Message})
		if o.Problem == "" && c.Status == corev1.ConditionFalse && c.Reason != "ScalingDisabled" &&
			(c.Type == autoscalingv2.AbleToScale || c.Type == autoscalingv2.ScalingActive) {
			o.Problem = c.Reason
		}
	}
	return o
}

func hpaMetric(m autoscalingv2.MetricSpec, st autoscalingv2.MetricStatus) HPAMetric {
	var name string
	var target autoscalingv2.MetricTarget
	var current *autoscalingv2.MetricValueStatus
	switch {
	case m.Resource != nil:
		name, target = string(m.Resource.Name), m.Resource.Target
		if st.Resource != nil {
			current = &st.Resource.Current
		}
	case m.ContainerResource != nil:
		name, target = fmt.Sprintf("%s of %s", m.ContainerResource.Name, m.ContainerResource.Container), m.ContainerResource.Target
		if st.ContainerResource != nil {
			current = &st.ContainerResource.Current
		}
	case m.Pods != nil:
		name, target = m.Pods.Metric.Name, m.Pods.Target
		if st.Pods != nil {
			current = &st.Pods.Current
		}
	case m.Object != nil:
		name, target = fmt.Sprintf("%s of %s/%s", m.Object.Metric.Name, m.Object.DescribedObject.Kind, m.Object.DescribedObject.Name), m.Object.Target
		if st.Object != nil {
			current = &st.Object.Current
		}
	case m.External != nil:
		name, target = m.External.Metric.Name, m.External.Target
		if st.External != nil {
			current = &st.External.Current
		}
	}
	o := HPAMetric{Name: name, Target: metricValue(target.AverageUtilization, target.AverageValue, target.Value)}
	if current != nil {
		o.Current = metricValue(current.AverageUtilization, current.AverageValue, current.Value)
	}
	return o
}

// metricValue is a utilization as a percentage, or a quantity; a target sets one of them and its status reports the same.
func metricValue(utilization *int32, average, value *resource.Quantity) string {
	switch {
	case utilization != nil:
		return fmt.Sprintf("%d%%", *utilization)
	case average != nil:
		return average.String()
	case value != nil:
		return value.String()
	}
	return ""
}
