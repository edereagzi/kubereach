package service_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/edereagzi/kubereach/internal/service"
	"github.com/google/go-cmp/cmp"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	k8stesting "k8s.io/client-go/testing"
)

// eventEvents routes event stream events into channels, leaving other events to the previous Emit.
func eventEvents(svc *service.Service) (<-chan service.EventBatch, <-chan service.EventStatus) {
	batches := make(chan service.EventBatch, 100)
	states := make(chan service.EventStatus, 100)
	emit := svc.Emit
	svc.Emit = func(name string, data any) {
		switch data := data.(type) {
		case service.EventBatch:
			batches <- data
		case service.EventStatus:
			states <- data
		default:
			emit(name, data)
		}
	}
	return batches, states
}

func collectEvents(t *testing.T, batches <-chan service.EventBatch, streamID string, n int) []service.KubeEvent {
	t.Helper()
	var events []service.KubeEvent
	deadline := time.After(10 * time.Second)
	for len(events) < n {
		select {
		case b := <-batches:
			if b.StreamID != streamID {
				t.Fatalf("batch for stream %q, want %q", b.StreamID, streamID)
			}
			events = append(events, b.Events...)
		case <-deadline:
			t.Fatalf("timed out with %d of %d events: %v", len(events), n, events)
		}
	}
	return events
}

func waitEventState(t *testing.T, states <-chan service.EventStatus, want service.State) service.EventStatus {
	t.Helper()
	var seen []service.EventStatus
	for {
		select {
		case st := <-states:
			if st.State == want {
				return st
			}
			seen = append(seen, st)
		case <-time.After(10 * time.Second):
			t.Fatalf("timed out waiting for event state %s, seen %+v", want, seen)
		}
	}
}

func TestEvents_WatchDeliversNewestFirstAndFollowsChanges(t *testing.T) {
	f := newForwardFixture(t,
		event("default", "api-0", "Normal", "Pulled", "Container image already present", 6, diagEpoch.Add(-time.Minute)),
		event("default", "api-0", "Warning", "BackOff", "Back-off restarting failed container", 12, diagEpoch.Add(time.Minute)),
	)
	batches, states := eventEvents(f.svc)
	ctx := context.Background()

	stream, err := f.svc.StartEvents(f.cluster)
	if err != nil {
		t.Fatal(err)
	}
	if stream.ID == "" || stream.ClusterID != f.cluster {
		t.Fatalf("stream = %+v, want an ID and the cluster", stream)
	}
	waitEventState(t, states, service.StateConnected)

	backlog := collectEvents(t, batches, stream.ID, 2)
	want := []service.KubeEvent{
		{ID: "default/api-0.BackOff", Kind: "Pod", Namespace: "default", Name: "api-0", Type: "Warning", Reason: "BackOff", Message: "Back-off restarting failed container", Count: 12, Time: diagEpoch.Add(time.Minute)},
		{ID: "default/api-0.Pulled", Kind: "Pod", Namespace: "default", Name: "api-0", Type: "Normal", Reason: "Pulled", Message: "Container image already present", Count: 6, Time: diagEpoch.Add(-time.Minute)},
	}
	if diff := cmp.Diff(want, backlog); diff != "" {
		t.Errorf("backlog (-want +got):\n%s", diff)
	}
	var warnings []string
	for _, e := range backlog {
		if e.Type == "Warning" {
			warnings = append(warnings, e.Reason)
		}
	}
	if diff := cmp.Diff([]string{"BackOff"}, warnings); diff != "" {
		t.Errorf("warnings (-want +got):\n%s", diff)
	}

	// A new event arrives live; a repeated one arrives again under the same ID with its new count.
	ev := event("default", "worker", "Warning", "FailedScheduling", "0/3 nodes are available", 1, diagEpoch.Add(2*time.Minute))
	ev.InvolvedObject.Kind = "Deployment"
	if _, err := f.cs.CoreV1().Events("default").Create(ctx, ev, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	live := collectEvents(t, batches, stream.ID, 1)
	if live[0].ID != "default/worker.FailedScheduling" || live[0].Kind != "Deployment" || live[0].Name != "worker" || live[0].Count != 1 {
		t.Errorf("live event = %+v, want the deployment's FailedScheduling", live[0])
	}
	ev.Count = 2
	ev.LastTimestamp = metav1.NewTime(diagEpoch.Add(3 * time.Minute))
	if _, err := f.cs.CoreV1().Events("default").Update(ctx, ev, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	repeated := collectEvents(t, batches, stream.ID, 1)
	if repeated[0].ID != live[0].ID || repeated[0].Count != 2 || !repeated[0].Time.Equal(diagEpoch.Add(3*time.Minute)) {
		t.Errorf("repeated event = %+v, want count 2 at +3m under %s", repeated[0], live[0].ID)
	}

	// A second start for the same Cluster replaces the first, so a reloaded UI never leaves a watch behind.
	again, err := f.svc.StartEvents(f.cluster)
	if err != nil {
		t.Fatal(err)
	}
	if st := waitEventState(t, states, service.StateStopped); st.ID != stream.ID {
		t.Errorf("stopped stream = %s, want the first stream %s", st.ID, stream.ID)
	}
	if err := f.svc.StopEvents(again.ID); err != nil {
		t.Fatal(err)
	}
	if st := waitEventState(t, states, service.StateStopped); st.ID != again.ID {
		t.Errorf("stopped stream = %s, want %s", st.ID, again.ID)
	}
	if err := f.svc.StopEvents(again.ID); err != nil {
		t.Errorf("second stop = %v, want nil", err)
	}
}

// A forbidden namespace in the scope is shown as such and stays shown while the permitted one keeps delivering.
func TestEvents_ForbiddenNamespaceIsReportedBesidePermittedOne(t *testing.T) {
	f := newForwardFixture(t, event("default", "api-0", "Warning", "BackOff", "crashing", 1, diagEpoch))
	batches, states := eventEvents(f.svc)
	if err := f.svc.SetNamespaces(f.cluster, []string{"default", "locked"}); err != nil {
		t.Fatal(err)
	}
	f.cs.PrependReactor("list", "events", func(a k8stesting.Action) (bool, runtime.Object, error) {
		if a.GetNamespace() != "locked" {
			return false, nil, nil
		}
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "events"}, "", errors.New("rbac"))
	})
	stream, err := f.svc.StartEvents(f.cluster)
	if err != nil {
		t.Fatal(err)
	}
	st := waitEventState(t, states, service.StateError)
	if !strings.Contains(st.Error, "forbidden") {
		t.Errorf("status = %+v, want a forbidden error", st)
	}
	if got := collectEvents(t, batches, stream.ID, 1); got[0].ID != "default/api-0.BackOff" {
		t.Errorf("events = %+v, want the permitted namespace's", got)
	}
	// Nothing after the forbidden report moves the state back to connected.
	select {
	case st := <-states:
		if st.State != service.StateStopped {
			t.Errorf("state after forbidden = %+v, want it to stay in error", st)
		}
	case <-time.After(500 * time.Millisecond):
	}
	_ = f.svc.StopEvents(stream.ID)
}
