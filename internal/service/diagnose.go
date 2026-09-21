package service

import (
	"context"
	"fmt"
	"slices"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
)

// PodDiagnosis is why a pod is in its state: phase, conditions, each container's state and the pod's events newest first.
type PodDiagnosis struct {
	Namespace  string               `json:"namespace"`
	Name       string               `json:"name"`
	Phase      string               `json:"phase"`
	Reason     string               `json:"reason"`
	Node       string               `json:"node"`
	Conditions []PodCondition       `json:"conditions"`
	Containers []ContainerDiagnosis `json:"containers"`
	// Events is nil and EventsError set when the events could not be listed; the rest of the diagnosis still stands.
	Events      []KubeEvent `json:"events"`
	EventsError string      `json:"eventsError,omitempty"`
}

type PodCondition struct {
	Type    string `json:"type"`
	Status  string `json:"status"`
	Reason  string `json:"reason,omitempty"`
	Message string `json:"message,omitempty"`
}

type ContainerDiagnosis struct {
	Name     string `json:"name"`
	Init     bool   `json:"init,omitempty"`
	Image    string `json:"image"`
	Ready    bool   `json:"ready"`
	Restarts int32  `json:"restarts"`
	// State is empty when the container has never been started; LastState is the previous run, whose logs can be read.
	State     ContainerState    `json:"state"`
	LastState *ContainerState   `json:"lastState,omitempty"`
	Requests  map[string]string `json:"requests,omitempty"`
	Limits    map[string]string `json:"limits,omitempty"`
}

// ContainerState is one run: Status is running, waiting or terminated; ExitCode and the times apply to a terminated run.
type ContainerState struct {
	Status     string    `json:"status"`
	Reason     string    `json:"reason,omitempty"`
	Message    string    `json:"message,omitempty"`
	ExitCode   int32     `json:"exitCode"`
	StartedAt  time.Time `json:"startedAt"`
	FinishedAt time.Time `json:"finishedAt"`
}

// KubeEvent is one event; ID is the event object's own namespace/name, so a repeated event replaces its earlier delivery.
type KubeEvent struct {
	ID string `json:"id"`
	// Kind, Namespace and Name are the involved object.
	Kind      string    `json:"kind"`
	Namespace string    `json:"namespace"`
	Name      string    `json:"name"`
	Type      string    `json:"type"`
	Reason    string    `json:"reason"`
	Message   string    `json:"message"`
	Count     int32     `json:"count"`
	Time      time.Time `json:"time"`
}

// DescribePod explains one pod's state; events it cannot list are reported in EventsError rather than failing.
func (s *Service) DescribePod(ctx context.Context, clusterID, namespace, name string) (PodDiagnosis, error) {
	k, err := s.clusterClient(clusterID)
	if err != nil {
		return PodDiagnosis{}, err
	}
	pod, err := k.client.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return PodDiagnosis{}, wrapForbidden(err)
	}
	d := PodDiagnosis{Namespace: pod.Namespace, Name: pod.Name, Phase: string(pod.Status.Phase), Reason: PodReason(pod), Node: pod.Spec.NodeName}
	for _, c := range pod.Status.Conditions {
		d.Conditions = append(d.Conditions, PodCondition{Type: string(c.Type), Status: string(c.Status), Reason: c.Reason, Message: c.Message})
	}
	d.Containers = append(describeContainers(pod.Spec.InitContainers, pod.Status.InitContainerStatuses, true), describeContainers(pod.Spec.Containers, pod.Status.ContainerStatuses, false)...)
	d.Events, err = objectEvents(ctx, k, "Pod", pod.Namespace, pod.Name, string(pod.UID))
	if err != nil {
		d.EventsError = err.Error()
	}
	return d, nil
}

func describeContainers(specs []corev1.Container, statuses []corev1.ContainerStatus, init bool) []ContainerDiagnosis {
	var out []ContainerDiagnosis
	for _, c := range specs {
		cd := ContainerDiagnosis{Name: c.Name, Init: init, Image: c.Image, Requests: quantities(c.Resources.Requests), Limits: quantities(c.Resources.Limits)}
		if i := slices.IndexFunc(statuses, func(st corev1.ContainerStatus) bool { return st.Name == c.Name }); i >= 0 {
			st := statuses[i]
			cd.Ready, cd.Restarts = st.Ready, st.RestartCount
			cd.State = containerState(st.State)
			if last := containerState(st.LastTerminationState); last.Status != "" {
				cd.LastState = &last
			}
		}
		out = append(out, cd)
	}
	return out
}

func quantities(list corev1.ResourceList) map[string]string {
	if len(list) == 0 {
		return nil
	}
	out := make(map[string]string, len(list))
	for name, q := range list {
		out[string(name)] = q.String()
	}
	return out
}

func containerState(st corev1.ContainerState) ContainerState {
	switch {
	case st.Running != nil:
		return ContainerState{Status: "running", StartedAt: st.Running.StartedAt.Time}
	case st.Waiting != nil:
		return ContainerState{Status: "waiting", Reason: st.Waiting.Reason, Message: st.Waiting.Message}
	case st.Terminated != nil:
		t := st.Terminated
		return ContainerState{Status: "terminated", Reason: t.Reason, Message: t.Message, ExitCode: t.ExitCode, StartedAt: t.StartedAt.Time, FinishedAt: t.FinishedAt.Time}
	}
	return ContainerState{}
}

// objectEvents lists one object's events newest first.
func objectEvents(ctx context.Context, k kube, kind, namespace, name, uid string) ([]KubeEvent, error) {
	selector := fields.Set{"involvedObject.kind": kind, "involvedObject.name": name, "involvedObject.uid": uid}.AsSelector().String()
	list, err := k.client.CoreV1().Events(namespace).List(ctx, metav1.ListOptions{FieldSelector: selector})
	if err != nil {
		return nil, wrapForbidden(err)
	}
	out := []KubeEvent{}
	for _, e := range list.Items {
		// ponytail: the fake clientset ignores field selectors, so kind and name are re-checked here; a reactor in the tests would remove this.
		if e.InvolvedObject.Kind != kind || e.InvolvedObject.Name != name {
			continue
		}
		out = append(out, newKubeEvent(&e))
	}
	sortEventsNewestFirst(out)
	return out, nil
}

func newKubeEvent(e *corev1.Event) KubeEvent {
	o := e.InvolvedObject
	return KubeEvent{ID: e.Namespace + "/" + e.Name, Kind: o.Kind, Namespace: o.Namespace, Name: o.Name, Type: e.Type, Reason: e.Reason, Message: e.Message, Count: max(e.Count, 1), Time: eventTime(*e)}
}

func sortEventsNewestFirst(events []KubeEvent) {
	slices.SortStableFunc(events, func(a, b KubeEvent) int { return b.Time.Compare(a.Time) })
}

// eventTime is when the event was last seen; the events.k8s.io shape fills EventTime and series instead of the legacy stamps.
func eventTime(e corev1.Event) time.Time {
	if e.Series != nil {
		return e.Series.LastObservedTime.Time
	}
	if !e.LastTimestamp.IsZero() {
		return e.LastTimestamp.Time
	}
	if !e.EventTime.IsZero() {
		return e.EventTime.Time
	}
	return e.FirstTimestamp.Time
}

// PodReason is the one word that says what is wrong with a pod, in kubectl's order of precedence;
// it is empty for a pod that is running and ready or has completed.
func PodReason(pod *corev1.Pod) string {
	if pod.DeletionTimestamp != nil {
		return "Terminating"
	}
	if pod.Status.Reason != "" {
		return pod.Status.Reason
	}
	for _, st := range pod.Status.InitContainerStatuses {
		if r := containerReason(st); r != "" {
			return "Init:" + r
		}
	}
	// The first container listed has the last word, as in kubectl, so a sidecar's reason does not hide the app's.
	for _, st := range pod.Status.ContainerStatuses {
		if r := containerReason(st); r != "" {
			return r
		}
	}
	switch pod.Status.Phase {
	case corev1.PodRunning:
		if slices.ContainsFunc(pod.Status.ContainerStatuses, func(st corev1.ContainerStatus) bool { return !st.Ready }) {
			return "NotReady"
		}
		return ""
	case corev1.PodSucceeded:
		return ""
	case corev1.PodPending:
		for _, c := range pod.Status.Conditions {
			if c.Type == corev1.PodScheduled && c.Status == corev1.ConditionFalse && c.Reason != "" {
				return c.Reason
			}
		}
	}
	return string(pod.Status.Phase)
}

// containerReason names what a container is stuck on; a clean exit is nothing to report, and a crash loop names
// the kill that caused it when it was the OOM killer.
func containerReason(st corev1.ContainerStatus) string {
	if w := st.State.Waiting; w != nil {
		if last := st.LastTerminationState.Terminated; w.Reason == "CrashLoopBackOff" && last != nil && last.Reason == "OOMKilled" {
			return last.Reason
		}
		return w.Reason
	}
	if t := st.State.Terminated; t != nil {
		switch {
		case t.ExitCode == 0 && t.Signal == 0:
			return ""
		case t.Reason != "":
			return t.Reason
		case t.Signal != 0:
			return fmt.Sprintf("Signal:%d", t.Signal)
		default:
			return fmt.Sprintf("ExitCode:%d", t.ExitCode)
		}
	}
	return ""
}

func podRestarts(pod *corev1.Pod) int32 {
	var n int32
	for _, st := range pod.Status.ContainerStatuses {
		n += st.RestartCount
	}
	return n
}
