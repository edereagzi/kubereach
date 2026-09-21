package service_test

import (
	"context"
	"testing"
	"time"

	"github.com/edereagzi/kubereach/internal/service"
	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var diagEpoch = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

func event(ns, pod, kind, reason, message string, count int32, last time.Time) *corev1.Event {
	return &corev1.Event{
		ObjectMeta:     metav1.ObjectMeta{Namespace: ns, Name: pod + "." + reason},
		InvolvedObject: corev1.ObjectReference{Kind: "Pod", Namespace: ns, Name: pod},
		Type:           kind,
		Reason:         reason,
		Message:        message,
		Count:          count,
		LastTimestamp:  metav1.NewTime(last),
	}
}

func crashLoopPod(ns, name string, lastReason string, exitCode int32) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		Spec: corev1.PodSpec{NodeName: "node-1", Containers: []corev1.Container{{
			Name:  "app",
			Image: "api:1.2",
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m"), corev1.ResourceMemory: resource.MustParse("64Mi")},
				Limits:   corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("128Mi")},
			},
		}}},
		Status: corev1.PodStatus{
			Phase:      corev1.PodRunning,
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionFalse, Reason: "ContainersNotReady", Message: "containers with unready status: [app]"}},
			ContainerStatuses: []corev1.ContainerStatus{{
				Name:         "app",
				Image:        "api:1.2",
				RestartCount: 5,
				State:        corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff", Message: "back-off 5m0s restarting failed container"}},
				LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
					Reason: lastReason, ExitCode: exitCode, StartedAt: metav1.NewTime(diagEpoch), FinishedAt: metav1.NewTime(diagEpoch.Add(3 * time.Second)),
				}},
			}},
		},
	}
}

func TestDescribePod_CrashLooping(t *testing.T) {
	svc, _, id := newFakeService(t,
		crashLoopPod("default", "api-0", "Error", 1),
		event("default", "api-0", "Normal", "Pulled", "Container image already present", 6, diagEpoch.Add(-time.Minute)),
		event("default", "api-0", "Warning", "BackOff", "Back-off restarting failed container", 12, diagEpoch.Add(time.Minute)),
		event("default", "other", "Warning", "BackOff", "not this pod", 1, diagEpoch.Add(time.Hour)),
	)
	ctx := context.Background()

	got, err := svc.DescribePod(ctx, id, "default", "api-0")
	if err != nil {
		t.Fatal(err)
	}
	want := service.PodDiagnosis{
		Namespace: "default", Name: "api-0", Phase: "Running", Reason: "CrashLoopBackOff", Node: "node-1",
		Conditions: []service.PodCondition{{Type: "Ready", Status: "False", Reason: "ContainersNotReady", Message: "containers with unready status: [app]"}},
		Containers: []service.ContainerDiagnosis{{
			Name: "app", Image: "api:1.2", Restarts: 5,
			State:     service.ContainerState{Status: "waiting", Reason: "CrashLoopBackOff", Message: "back-off 5m0s restarting failed container"},
			LastState: &service.ContainerState{Status: "terminated", Reason: "Error", ExitCode: 1, StartedAt: diagEpoch, FinishedAt: diagEpoch.Add(3 * time.Second)},
			Requests:  map[string]string{"cpu": "100m", "memory": "64Mi"},
			Limits:    map[string]string{"memory": "128Mi"},
		}},
		Events: []service.KubeEvent{
			{ID: "default/api-0.BackOff", Kind: "Pod", Namespace: "default", Name: "api-0", Type: "Warning", Reason: "BackOff", Message: "Back-off restarting failed container", Count: 12, Time: diagEpoch.Add(time.Minute)},
			{ID: "default/api-0.Pulled", Kind: "Pod", Namespace: "default", Name: "api-0", Type: "Normal", Reason: "Pulled", Message: "Container image already present", Count: 6, Time: diagEpoch.Add(-time.Minute)},
		},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("diagnosis mismatch (-want +got):\n%s", diff)
	}

	pods, err := svc.ListPods(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(pods) != 1 || pods[0].Reason != "CrashLoopBackOff" || pods[0].Restarts != 5 {
		t.Errorf("pod row = %+v, want reason CrashLoopBackOff with 5 restarts", pods)
	}
}

func TestDescribePod_OOMKilledIsTheReason(t *testing.T) {
	svc, _, id := newFakeService(t, crashLoopPod("default", "api-0", "OOMKilled", 137))

	got, err := svc.DescribePod(context.Background(), id, "default", "api-0")
	if err != nil {
		t.Fatal(err)
	}
	if got.Reason != "OOMKilled" || got.Containers[0].LastState.ExitCode != 137 || got.Containers[0].Limits["memory"] != "128Mi" {
		t.Errorf("diagnosis = %+v, want OOMKilled with exit 137 and the memory limit", got)
	}
	if got.Events == nil || got.EventsError != "" {
		t.Errorf("events = %v (%q), want an empty list without error", got.Events, got.EventsError)
	}
}

func TestDescribePod_Unschedulable(t *testing.T) {
	pending := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "api-0"},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app"}}},
		Status: corev1.PodStatus{
			Phase:      corev1.PodPending,
			Conditions: []corev1.PodCondition{{Type: corev1.PodScheduled, Status: corev1.ConditionFalse, Reason: "Unschedulable", Message: "0/3 nodes are available: insufficient memory"}},
		},
	}
	svc, cs, id := newFakeService(t, pending, event("default", "api-0", "Warning", "FailedScheduling", "0/3 nodes are available", 3, diagEpoch))

	got, err := svc.DescribePod(context.Background(), id, "default", "api-0")
	if err != nil {
		t.Fatal(err)
	}
	if got.Reason != "Unschedulable" || got.Phase != "Pending" {
		t.Errorf("reason = %q phase = %q, want Unschedulable/Pending", got.Reason, got.Phase)
	}
	// A container that never started still lists, without state.
	if len(got.Containers) != 1 || got.Containers[0].State.Status != "" || got.Containers[0].LastState != nil {
		t.Errorf("containers = %+v, want app with no state", got.Containers)
	}
	if len(got.Events) != 1 || got.Events[0].Reason != "FailedScheduling" {
		t.Errorf("events = %+v, want FailedScheduling", got.Events)
	}

	forbid(cs, "list", "events", false)
	got, err = svc.DescribePod(context.Background(), id, "default", "api-0")
	if err != nil {
		t.Fatal(err)
	}
	if got.Events != nil || got.EventsError == "" {
		t.Errorf("forbidden events = %v (%q), want none with an error", got.Events, got.EventsError)
	}
	if _, err := svc.DescribePod(context.Background(), id, "default", "missing"); err == nil {
		t.Fatal("missing pod described without error")
	}
}

func TestPodReason_Table(t *testing.T) {
	running := func(ready bool) corev1.ContainerStatus {
		return corev1.ContainerStatus{Name: "app", Ready: ready, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}
	}
	cases := []struct {
		name string
		pod  corev1.Pod
		want string
	}{
		{"healthy", corev1.Pod{Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{running(true)}}}, ""},
		{"not ready", corev1.Pod{Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{running(false)}}}, "NotReady"},
		{"succeeded", corev1.Pod{Status: corev1.PodStatus{Phase: corev1.PodSucceeded}}, ""},
		{"pending", corev1.Pod{Status: corev1.PodStatus{Phase: corev1.PodPending}}, "Pending"},
		{"evicted", corev1.Pod{Status: corev1.PodStatus{Phase: corev1.PodFailed, Reason: "Evicted"}}, "Evicted"},
		{"terminating", corev1.Pod{ObjectMeta: metav1.ObjectMeta{DeletionTimestamp: &metav1.Time{Time: diagEpoch}}, Status: corev1.PodStatus{Phase: corev1.PodRunning}}, "Terminating"},
		{"image pull", corev1.Pod{Status: corev1.PodStatus{Phase: corev1.PodPending, ContainerStatuses: []corev1.ContainerStatus{{
			State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff"}},
		}}}}, "ImagePullBackOff"},
		{"init failing", corev1.Pod{Status: corev1.PodStatus{Phase: corev1.PodPending, InitContainerStatuses: []corev1.ContainerStatus{{
			State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 2}},
		}}}}, "Init:ExitCode:2"},
		{"init done", corev1.Pod{Status: corev1.PodStatus{Phase: corev1.PodRunning,
			InitContainerStatuses: []corev1.ContainerStatus{{State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0, Reason: "Completed"}}}},
			ContainerStatuses:     []corev1.ContainerStatus{running(true)},
		}}, ""},
		{"app reason wins over sidecar", corev1.Pod{Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{
			{Name: "app", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}}},
			{Name: "sidecar", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "PodInitializing"}}},
		}}}, "CrashLoopBackOff"},
		{"sidecar exited cleanly", corev1.Pod{Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{
			running(true),
			{Name: "sidecar", Ready: false, State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0, Reason: "Completed"}}},
		}}}, "NotReady"},
	}
	for _, tc := range cases {
		if got := service.PodReason(&tc.pod); got != tc.want {
			t.Errorf("%s: reason = %q, want %q", tc.name, got, tc.want)
		}
	}
}
