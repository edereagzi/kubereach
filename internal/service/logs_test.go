package service_test

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/edereagzi/kubereach/internal/service"
	"github.com/google/go-cmp/cmp"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var logEpoch = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func logKey(pod, container string) string { return pod + "/" + container }

// logHandler speaks the log subresource: the container's seeded lines are written with their timestamps,
// then the body follows a feed until the client goes away.
func (a *testAPI) logHandler(w http.ResponseWriter, r *http.Request, pod string) {
	container := r.URL.Query().Get("container")
	if r.URL.Query().Get("follow") != "true" || r.URL.Query().Get("timestamps") != "true" {
		http.Error(w, "expected follow=true&timestamps=true", http.StatusBadRequest)
		return
	}
	a.mu.Lock()
	gone := a.gone[pod]
	seeded := a.logs[logKey(pod, container)]
	feed := a.feed(pod, container)
	a.mu.Unlock()
	if gone {
		http.Error(w, "pod not found", http.StatusNotFound)
		return
	}
	if seeded == nil {
		http.Error(w, "container not found", http.StatusBadRequest)
		return
	}
	flusher := w.(http.Flusher)
	for _, line := range seeded {
		_, _ = fmt.Fprintln(w, line)
	}
	flusher.Flush()
	for {
		select {
		case <-r.Context().Done():
			return
		case text, ok := <-feed:
			if !ok {
				return
			}
			_, _ = fmt.Fprintf(w, "%s %s\n", time.Now().UTC().Format(time.RFC3339Nano), text)
			flusher.Flush()
		}
	}
}

// feed returns the live line channel for a container; callers hold a.mu.
func (a *testAPI) feed(pod, container string) chan string {
	key := logKey(pod, container)
	if a.feeds[key] == nil {
		a.feeds[key] = make(chan string, 10)
	}
	return a.feeds[key]
}

// seedLogs stores backlog lines stamped one second apart, continuing from the last seeded line of any container.
func (a *testAPI) seedLogs(pod, container string, lines ...string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, text := range lines {
		stamp := logEpoch.Add(time.Duration(a.seeded) * time.Second).Format(time.RFC3339Nano)
		a.logs[logKey(pod, container)] = append(a.logs[logKey(pod, container)], stamp+" "+text)
		a.seeded++
	}
}

// deletePod ends every follow body of the pod and makes later log requests fail as not found, as the API server does.
func (a *testAPI) deletePod(pod string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.gone[pod] = true
	for key, feed := range a.feeds {
		if strings.HasPrefix(key, pod+"/") {
			close(feed)
			delete(a.feeds, key)
		}
	}
}

func (a *testAPI) writeLog(pod, container, text string) {
	a.mu.Lock()
	feed := a.feed(pod, container)
	a.mu.Unlock()
	feed <- text
}

func twoContainerPod(ns, name string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, Labels: map[string]string{"app": "api"}},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app"}, {Name: "sidecar"}}},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning},
	}
}

func deployment(ns, name string) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": name}},
			Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app"}, {Name: "sidecar"}}}},
		},
	}
}

// logEvents routes log events into channels, leaving other events to the previous Emit.
func logEvents(svc *service.Service) (<-chan service.LogBatch, <-chan service.LogStatus) {
	batches := make(chan service.LogBatch, 100)
	states := make(chan service.LogStatus, 100)
	emit := svc.Emit
	svc.Emit = func(name string, data any) {
		switch data := data.(type) {
		case service.LogBatch:
			batches <- data
		case service.LogStatus:
			states <- data
		default:
			emit(name, data)
		}
	}
	return batches, states
}

func collectLogs(t *testing.T, batches <-chan service.LogBatch, streamID string, n int) []service.LogLine {
	t.Helper()
	var lines []service.LogLine
	deadline := time.After(10 * time.Second)
	for len(lines) < n {
		select {
		case b := <-batches:
			if b.StreamID != streamID {
				t.Fatalf("batch for stream %q, want %q", b.StreamID, streamID)
			}
			lines = append(lines, b.Lines...)
		case <-deadline:
			t.Fatalf("timed out with %d of %d lines: %v", len(lines), n, lines)
		}
	}
	return lines
}

func texts(lines []service.LogLine) []string {
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		out = append(out, l.Container+": "+l.Text)
	}
	return out
}

func waitLogState(t *testing.T, states <-chan service.LogStatus, want service.State) service.LogStatus {
	t.Helper()
	return waitLogStatus(t, states, func(st service.LogStatus) bool { return st.State == want })
}

func waitLogPods(t *testing.T, states <-chan service.LogStatus, want ...string) service.LogStatus {
	t.Helper()
	return waitLogStatus(t, states, func(st service.LogStatus) bool { return slices.Equal(st.Pods, want) })
}

func waitLogStatus(t *testing.T, states <-chan service.LogStatus, ok func(service.LogStatus) bool) service.LogStatus {
	t.Helper()
	var seen []service.LogStatus
	for {
		select {
		case st := <-states:
			if ok(st) {
				return st
			}
			seen = append(seen, st)
		case <-time.After(10 * time.Second):
			t.Fatalf("timed out waiting for log status, seen %+v", seen)
		}
	}
}

func TestLogs_StreamsEveryContainerLabelledAndOrdered(t *testing.T) {
	f := newForwardFixture(t, twoContainerPod("default", "api-0"))
	batches, states := logEvents(f.svc)
	f.api.seedLogs("api-0", "app", "app 1", "app 2")
	f.api.seedLogs("api-0", "sidecar", "side 1", "side 2")
	f.api.seedLogs("api-0", "app", "app 3")

	ctx := context.Background()
	src := service.LogSource{ClusterID: f.cluster, Namespace: "default", Kind: service.LogSourcePod, Name: "api-0"}
	stream, err := f.svc.StartLogs(ctx, src)
	if err != nil {
		t.Fatal(err)
	}
	if stream.ID == "" || !slices.Equal(stream.Containers, []string{"app", "sidecar"}) || !slices.Equal(stream.Pods, []string{"api-0"}) {
		t.Fatalf("stream = %+v, want an ID, the pod and both containers", stream)
	}

	lines := collectLogs(t, batches, stream.ID, 5)
	wantOrder := []string{"app: app 1", "app: app 2", "sidecar: side 1", "sidecar: side 2", "app: app 3"}
	if diff := cmp.Diff(wantOrder, texts(lines)); diff != "" {
		t.Errorf("merged backlog (-want +got):\n%s", diff)
	}
	want := service.LogLine{Pod: "api-0", Container: "app", Time: logEpoch, Text: "app 1"}
	if diff := cmp.Diff(want, lines[0]); diff != "" {
		t.Errorf("first line (-want +got):\n%s", diff)
	}

	// The fixture's request timeout has passed by now; a follow must outlive it.
	time.Sleep(2 * fixtureTimeout)
	f.api.writeLog("api-0", "sidecar", "side 3")
	live := collectLogs(t, batches, stream.ID, 1)
	if live[0].Container != "sidecar" || live[0].Text != "side 3" || time.Since(live[0].Time) > time.Minute {
		t.Errorf("live line = %+v, want a fresh sidecar/side 3", live[0])
	}

	statuses := f.svc.LogStatuses()
	if len(statuses) != 1 || statuses[0].ID != stream.ID || statuses[0].State != service.StateConnected {
		t.Errorf("statuses = %+v, want the connected stream", statuses)
	}
	if err := f.svc.StopLogs(stream.ID); err != nil {
		t.Fatal(err)
	}
	waitLogState(t, states, service.StateStopped)
	if got := f.svc.LogStatuses(); len(got) != 0 {
		t.Errorf("statuses after stop = %v, want none", got)
	}
}

func TestLogs_GonePodEndsStreamWhichStaysUntilStopped(t *testing.T) {
	f := newForwardFixture(t, twoContainerPod("default", "api-0"))
	batches, states := logEvents(f.svc)
	f.api.seedLogs("api-0", "app", "bye")
	f.api.seedLogs("api-0", "sidecar", "bye too")

	stream, err := f.svc.StartLogs(context.Background(), service.LogSource{ClusterID: f.cluster, Namespace: "default", Kind: service.LogSourcePod, Name: "api-0"})
	if err != nil {
		t.Fatal(err)
	}
	collectLogs(t, batches, stream.ID, 2)
	f.api.deletePod("api-0")

	ended := waitLogState(t, states, service.StateError)
	if !strings.Contains(ended.Error, "api-0") {
		t.Errorf("ended stream error = %q, want it to name the pod", ended.Error)
	}
	if got := f.svc.LogStatuses(); len(got) != 1 || got[0].State != service.StateError {
		t.Errorf("statuses after end = %+v, want the ended stream", got)
	}
	if err := f.svc.StopLogs(stream.ID); err != nil {
		t.Fatal(err)
	}
	waitLogState(t, states, service.StateStopped)
	if got := f.svc.LogStatuses(); len(got) != 0 {
		t.Errorf("statuses after stop = %v, want none", got)
	}
}

func TestLogs_JSONLinesCarryFields(t *testing.T) {
	f := newForwardFixture(t, twoContainerPod("default", "api-0"))
	batches, _ := logEvents(f.svc)
	f.api.seedLogs("api-0", "app", `{"level":"info","msg":"hello","status":200,"user":{"id":7},"nothing":null}`, `{not json`, "plain")
	f.api.seedLogs("api-0", "sidecar", "x")

	stream, err := f.svc.StartLogs(context.Background(), service.LogSource{ClusterID: f.cluster, Namespace: "default", Kind: service.LogSourcePod, Name: "api-0"})
	if err != nil {
		t.Fatal(err)
	}
	lines := collectLogs(t, batches, stream.ID, 4)
	want := map[string]string{"level": "info", "msg": "hello", "status": "200", "user": `{"id":7}`, "nothing": ""}
	if diff := cmp.Diff(want, lines[0].Fields); diff != "" {
		t.Errorf("fields (-want +got):\n%s", diff)
	}
	if lines[0].Text != `{"level":"info","msg":"hello","status":200,"user":{"id":7},"nothing":null}` {
		t.Errorf("json line text = %q, want the raw line", lines[0].Text)
	}
	for _, l := range lines[1:] {
		if l.Fields != nil {
			t.Errorf("line %q has fields %v, want none", l.Text, l.Fields)
		}
	}
	_ = f.svc.StopLogs(stream.ID)
}

func TestLogs_WorkloadFollowsPodsAsTheyComeAndGo(t *testing.T) {
	f := newForwardFixture(t, deployment("default", "api"), twoContainerPod("default", "api-a"), twoContainerPod("default", "api-b"),
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "other", Labels: map[string]string{"app": "other"}}})
	batches, states := logEvents(f.svc)
	f.api.seedLogs("api-a", "app", "a 1")
	f.api.seedLogs("api-a", "sidecar", "a side")
	f.api.seedLogs("api-b", "app", "b 1")
	f.api.seedLogs("api-b", "sidecar", "b side")
	f.api.seedLogs("api-c", "app", "c 1")
	f.api.seedLogs("api-c", "sidecar", "c side")

	ctx := context.Background()
	src := service.LogSource{ClusterID: f.cluster, Namespace: "default", Kind: service.LogSourceDeployment, Name: "api"}
	stream, err := f.svc.StartLogs(ctx, src)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(stream.Containers, []string{"app", "sidecar"}) {
		t.Fatalf("stream = %+v, want the template's containers", stream)
	}
	waitLogPods(t, states, "api-a", "api-b")
	lines := collectLogs(t, batches, stream.ID, 4)
	byPod := map[string][]string{}
	for _, l := range lines {
		byPod[l.Pod] = append(byPod[l.Pod], l.Container+": "+l.Text)
	}
	wantByPod := map[string][]string{"api-a": {"app: a 1", "sidecar: a side"}, "api-b": {"app: b 1", "sidecar: b side"}}
	if diff := cmp.Diff(wantByPod, byPod); diff != "" {
		t.Errorf("lines by pod (-want +got):\n%s", diff)
	}

	if _, err := f.cs.CoreV1().Pods("default").Create(ctx, twoContainerPod("default", "api-c"), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	waitLogPods(t, states, "api-a", "api-b", "api-c")
	joined := collectLogs(t, batches, stream.ID, 2)
	if joined[0].Pod != "api-c" || joined[1].Pod != "api-c" {
		t.Errorf("lines after join = %v, want api-c's", joined)
	}

	f.api.deletePod("api-b")
	if err := f.cs.CoreV1().Pods("default").Delete(ctx, "api-b", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	if st := waitLogPods(t, states, "api-a", "api-c"); st.State != service.StateConnected {
		t.Errorf("state after api-b left = %s (%s), want connected", st.State, st.Error)
	}
	f.api.writeLog("api-a", "app", "a 2")
	live := collectLogs(t, batches, stream.ID, 1)
	if live[0].Pod != "api-a" || live[0].Text != "a 2" {
		t.Errorf("live line = %+v, want api-a/a 2", live[0])
	}
	// Scaling to zero: the last bodies end before the pods go, and the stream settles back to connected with no pods.
	for _, pod := range []string{"api-a", "api-c"} {
		f.api.deletePod(pod)
		if err := f.cs.CoreV1().Pods("default").Delete(ctx, pod, metav1.DeleteOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	if st := waitLogPods(t, states); st.State != service.StateConnected {
		t.Errorf("state at zero pods = %s (%s), want connected", st.State, st.Error)
	}
	if err := f.svc.StopLogs(stream.ID); err != nil {
		t.Fatal(err)
	}
	waitLogState(t, states, service.StateStopped)
}

func TestLogs_ListWorkloads(t *testing.T) {
	f := newForwardFixture(t, deployment("default", "api"),
		&appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Namespace: "db", Name: "postgres"}},
		&appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Namespace: "kube-system", Name: "node-exporter"}})
	got, err := f.svc.ListWorkloads(context.Background(), f.cluster)
	if err != nil {
		t.Fatal(err)
	}
	want := []service.KubeWorkload{
		{Namespace: "db", Name: "postgres", Kind: service.LogSourceStatefulSet},
		{Namespace: "default", Name: "api", Kind: service.LogSourceDeployment},
		{Namespace: "kube-system", Name: "node-exporter", Kind: service.LogSourceDaemonSet},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("workloads (-want +got):\n%s", diff)
	}
}

func TestLogs_ResumeAfterRouteReconnects(t *testing.T) {
	priv, pub := newKeyPair(t)
	f := newRouteFixture(t, writeKeyFile(t, priv, ""), pub)
	batches, states := logEvents(f.svc)
	f.connect(t)
	f.podAPI.seedLogs("api-0", "app", "before")

	stream, err := f.svc.StartLogs(context.Background(), service.LogSource{ClusterID: f.cluster, Namespace: "default", Kind: service.LogSourcePod, Name: "api-0"})
	if err != nil {
		t.Fatal(err)
	}
	if got := texts(collectLogs(t, batches, stream.ID, 1)); got[0] != "app: before" {
		t.Fatalf("first line = %v", got)
	}
	waitLogState(t, states, service.StateConnected)

	f.ssh.dropConnections()
	f.waitState(t, service.StateReconnecting)
	waitLogState(t, states, service.StateReconnecting)
	f.waitState(t, service.StateConnected)
	waitLogState(t, states, service.StateConnected)

	// The backlog was already delivered; only the new line arrives after the resume.
	f.podAPI.writeLog("api-0", "app", "after")
	if got := texts(collectLogs(t, batches, stream.ID, 1)); got[0] != "app: after" {
		t.Errorf("line after resume = %v, want only app: after", got)
	}
	if err := f.svc.StopLogs(stream.ID); err != nil {
		t.Fatal(err)
	}
}

func TestLogs_MissingPodFails(t *testing.T) {
	f := newForwardFixture(t)
	_, err := f.svc.StartLogs(context.Background(), service.LogSource{ClusterID: f.cluster, Namespace: "default", Kind: service.LogSourcePod, Name: "nope"})
	if err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("err = %v, want not found for pod nope", err)
	}
	if got := f.svc.LogStatuses(); len(got) != 0 {
		t.Errorf("failed start left statuses %v", got)
	}
}
