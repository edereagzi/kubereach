package service_test

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/edereagzi/kubereach/internal/service"
	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	"k8s.io/streaming/pkg/httpstream"
	"k8s.io/streaming/pkg/httpstream/spdy"
)

// testAPI serves any pod as running and speaks the API server's SPDY port-forward protocol:
// every data stream first receives "<pod>:<port>\n" and is then echoed, so a test can tell where bytes went.
type testAPI struct {
	mu    sync.Mutex
	conns map[string][]httpstream.Connection
}

func newTestAPI() *testAPI {
	return &testAPI{conns: map[string][]httpstream.Connection{}}
}

func (a *testAPI) portForwardHandler(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(r.URL.Path, "/")
	if len(parts) == 7 && parts[5] == "pods" && r.Method == http.MethodGet {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"apiVersion":"v1","kind":"Pod","metadata":{"namespace":%q,"name":%q},"status":{"phase":"Running"}}`, parts[4], parts[6])
		return
	}
	if len(parts) != 8 || parts[5] != "pods" || parts[7] != "portforward" {
		http.NotFound(w, r)
		return
	}
	pod := parts[6]
	if _, err := httpstream.Handshake(r, w, []string{"portforward.k8s.io"}); err != nil {
		return
	}
	conn := spdy.NewResponseUpgrader().UpgradeResponse(w, r, func(s httpstream.Stream, _ <-chan struct{}) error {
		if s.Headers().Get(corev1.StreamType) == corev1.StreamTypeData {
			go func() {
				defer func() { _ = s.Close() }()
				_, _ = fmt.Fprintf(s, "%s:%s\n", pod, s.Headers().Get(corev1.PortHeader))
				_, _ = io.Copy(s, s)
			}()
		}
		return nil
	})
	if conn == nil {
		return
	}
	a.mu.Lock()
	a.conns[pod] = append(a.conns[pod], conn)
	a.mu.Unlock()
	defer func() { _ = conn.Close() }()
	<-conn.CloseChan()
}

// killPod drops every port-forward connection to the pod, as the API server does when the pod is deleted.
func (a *testAPI) killPod(name string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, c := range a.conns[name] {
		_ = c.Close()
	}
	delete(a.conns, name)
}

func selectorService(ns, name string, port int32, targetPort intstr.IntOrString) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{"app": name},
			Ports:    []corev1.ServicePort{{Name: "http", Port: port, TargetPort: targetPort}},
		},
	}
}

func pod(ns, name, app string, phase corev1.PodPhase, ready bool, created time.Time) *corev1.Pod {
	cond := corev1.ConditionFalse
	if ready {
		cond = corev1.ConditionTrue
	}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, Labels: map[string]string{"app": app}, CreationTimestamp: metav1.NewTime(created)},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{
			Name:  "main",
			Ports: []corev1.ContainerPort{{Name: "http", ContainerPort: 8080}},
		}}},
		Status: corev1.PodStatus{
			Phase:      phase,
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: cond}},
		},
	}
}

type forwardFixture struct {
	svc     *service.Service
	cs      *fake.Clientset
	api     *testAPI
	events  chan service.ForwardStatus
	cluster string
}

// newForwardFixture lists with the fake clientset and forwards against an in-test API server speaking the port-forward protocol.
func newForwardFixture(t *testing.T, objects ...runtime.Object) *forwardFixture {
	t.Helper()
	f := &forwardFixture{cs: fake.NewClientset(objects...), api: newTestAPI(), events: make(chan service.ForwardStatus, 100)}
	api := httptest.NewServer(http.HandlerFunc(f.api.portForwardHandler))
	t.Cleanup(api.Close)
	f.svc = service.New(filepath.Join(t.TempDir(), "kubereach.yaml"), func(service.Cluster, service.DialFunc) (kubernetes.Interface, *rest.Config, error) {
		return f.cs, &rest.Config{Host: api.URL}, nil
	})
	f.svc.Emit = func(_ string, data any) {
		if status, ok := data.(service.ForwardStatus); ok {
			f.events <- status
		}
	}
	clusters, err := f.svc.ImportKubeconfigs([]string{writeKubeconfig(t)})
	if err != nil {
		t.Fatal(err)
	}
	f.cluster = clusters[0].ID
	return f
}

func (f *forwardFixture) waitState(t *testing.T, want service.State) service.ForwardStatus {
	t.Helper()
	return waitForwardState(t, f.events, want)
}

func waitForwardState(t *testing.T, events <-chan service.ForwardStatus, want service.State) service.ForwardStatus {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case ev := <-events:
			if ev.State == want {
				return ev
			}
			if ev.State == service.StateError {
				t.Fatalf("forward failed while waiting for %q: %s", want, ev.Error)
			}
		case <-deadline:
			t.Fatalf("timed out waiting for forward state %q", want)
		}
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port
}

// pingThroughPort sends one line through the local port and returns the lines that come back.
func pingThroughPort(t *testing.T, port int) []string {
	t.Helper()
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write([]byte("ping\n")); err != nil {
		t.Fatal(err)
	}
	r := bufio.NewReader(conn)
	var lines []string
	for range 2 {
		line, err := r.ReadString('\n')
		if err != nil {
			t.Fatalf("read through local port: %v (got %q so far)", err, lines)
		}
		lines = append(lines, strings.TrimSuffix(line, "\n"))
	}
	return lines
}

func TestForward_ServiceResolvesReadyPodLikeKubectl(t *testing.T) {
	now := time.Now()
	f := newForwardFixture(t,
		selectorService("default", "api", 80, intstr.FromString("http")),
		pod("default", "api-pending", "api", corev1.PodPending, false, now),
		pod("default", "api-notready", "api", corev1.PodRunning, false, now.Add(-time.Hour)),
		pod("default", "api-ready", "api", corev1.PodRunning, true, now),
		pod("default", "other", "other", corev1.PodRunning, true, now),
	)
	ctx := context.Background()

	pods, err := f.svc.ListPods(ctx, f.cluster)
	if err != nil {
		t.Fatal(err)
	}
	wantPods := []service.KubePod{
		{Namespace: "default", Name: "api-notready", Ports: []service.NamedPort{{Name: "http", Port: 8080}}},
		{Namespace: "default", Name: "api-pending", Ports: []service.NamedPort{{Name: "http", Port: 8080}}},
		{Namespace: "default", Name: "api-ready", Ports: []service.NamedPort{{Name: "http", Port: 8080}}},
		{Namespace: "default", Name: "other", Ports: []service.NamedPort{{Name: "http", Port: 8080}}},
	}
	if diff := cmp.Diff(wantPods, pods); diff != "" {
		t.Errorf("pods mismatch (-want +got):\n%s", diff)
	}

	local := freePort(t)
	pf, err := f.svc.StartForward(ctx, service.PortForward{
		ClusterID:  f.cluster,
		Target:     service.ForwardTarget{Kind: service.TargetService, Namespace: "default", Name: "api"},
		RemotePort: 80,
		LocalPort:  local,
	})
	if err != nil {
		t.Fatal(err)
	}
	if pf.ID == "" {
		t.Fatal("started forward has no ID")
	}
	f.waitState(t, service.StateConnecting)
	connected := f.waitState(t, service.StateConnected)
	if connected.Pod != "api-ready" {
		t.Errorf("resolved pod = %q, want api-ready", connected.Pod)
	}

	got := pingThroughPort(t, local)
	if diff := cmp.Diff([]string{"api-ready:8080", "ping"}, got); diff != "" {
		t.Errorf("bytes through local port mismatch (-want +got):\n%s", diff)
	}

	statuses := f.svc.ForwardStatuses()
	want := []service.ForwardStatus{{Forward: pf, Pod: "api-ready", State: service.StateConnected}}
	if diff := cmp.Diff(want, statuses); diff != "" {
		t.Errorf("statuses mismatch (-want +got):\n%s", diff)
	}

	if err := f.svc.StopForward(pf.ID); err != nil {
		t.Fatal(err)
	}
	f.waitState(t, service.StateStopped)
	if got := f.svc.ForwardStatuses(); len(got) != 0 {
		t.Errorf("statuses after stop = %v, want none", got)
	}
	if _, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", local), time.Second); err == nil {
		t.Error("local port still accepts connections after stop")
	}
}

func TestForward_ToPodUsesThatPod(t *testing.T) {
	f := newForwardFixture(t, pod("default", "api-ready", "api", corev1.PodRunning, true, time.Now()))
	local := freePort(t)
	pf, err := f.svc.StartForward(context.Background(), service.PortForward{
		ClusterID:  f.cluster,
		Target:     service.ForwardTarget{Kind: service.TargetPod, Namespace: "default", Name: "api-ready"},
		RemotePort: 9000,
		LocalPort:  local,
	})
	if err != nil {
		t.Fatal(err)
	}
	f.waitState(t, service.StateConnected)
	if diff := cmp.Diff([]string{"api-ready:9000", "ping"}, pingThroughPort(t, local)); diff != "" {
		t.Errorf("bytes through local port mismatch (-want +got):\n%s", diff)
	}
	if err := f.svc.StopForward(pf.ID); err != nil {
		t.Fatal(err)
	}
}

func TestForward_ServiceWithoutRunningPodFails(t *testing.T) {
	f := newForwardFixture(t,
		selectorService("default", "api", 80, intstr.FromInt32(8080)),
		pod("default", "api-pending", "api", corev1.PodPending, false, time.Now()),
	)
	_, err := f.svc.StartForward(context.Background(), service.PortForward{
		ClusterID:  f.cluster,
		Target:     service.ForwardTarget{Kind: service.TargetService, Namespace: "default", Name: "api"},
		RemotePort: 80,
		LocalPort:  freePort(t),
	})
	if err == nil {
		t.Fatal("expected error when no pod is running")
	}
	if got := f.svc.ForwardStatuses(); len(got) != 0 {
		t.Errorf("failed start left statuses %v", got)
	}
}

func TestForward_BusyLocalPortSuggestsFreeOne(t *testing.T) {
	f := newForwardFixture(t, pod("default", "api-ready", "api", corev1.PodRunning, true, time.Now()))
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = busy.Close() }()
	port := busy.Addr().(*net.TCPAddr).Port

	_, err = f.svc.StartForward(context.Background(), service.PortForward{
		ClusterID:  f.cluster,
		Target:     service.ForwardTarget{Kind: service.TargetPod, Namespace: "default", Name: "api-ready"},
		RemotePort: 8080,
		LocalPort:  port,
	})
	var inUse *service.PortInUseError
	if !errors.As(err, &inUse) {
		t.Fatalf("got %v, want PortInUseError", err)
	}
	if inUse.Port != port || inUse.Suggested == port || inUse.Suggested == 0 {
		t.Errorf("PortInUseError = %+v, want port %d and a different suggestion", inUse, port)
	}
	l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", inUse.Suggested))
	if err != nil {
		t.Fatalf("suggested port %d is not free: %v", inUse.Suggested, err)
	}
	_ = l.Close()
}

func TestForward_ThroughRoute(t *testing.T) {
	priv, pub := newKeyPair(t)
	f := newRouteFixture(t, writeKeyFile(t, priv, ""), pub)
	forwards := make(chan service.ForwardStatus, 100)
	emit := f.svc.Emit
	f.svc.Emit = func(name string, data any) {
		if status, ok := data.(service.ForwardStatus); ok {
			forwards <- status
			return
		}
		emit(name, data)
	}
	f.connect(t)

	local := freePort(t)
	pf, err := f.svc.StartForward(context.Background(), service.PortForward{
		ClusterID:  f.cluster,
		Target:     service.ForwardTarget{Kind: service.TargetPod, Namespace: "default", Name: "api-0"},
		RemotePort: 8080,
		LocalPort:  local,
	})
	if err != nil {
		t.Fatal(err)
	}
	waitForwardState(t, forwards, service.StateConnected)
	if diff := cmp.Diff([]string{"api-0:8080", "ping"}, pingThroughPort(t, local)); diff != "" {
		t.Errorf("bytes through local port mismatch (-want +got):\n%s", diff)
	}
	apiAddr := strings.TrimPrefix(f.api.URL, "http://")
	if got := f.ssh.forwardedTo(); len(got) == 0 || got[0] != apiAddr {
		t.Errorf("SSH server forwarded to %v, want [%s]", got, apiAddr)
	}
	if err := f.svc.StopForward(pf.ID); err != nil {
		t.Fatal(err)
	}
}

func TestForward_SavedForwardPersistsAndStartsByID(t *testing.T) {
	f := newForwardFixture(t, pod("default", "api-ready", "api", corev1.PodRunning, true, time.Now()))
	saved, err := f.svc.SaveForward(service.PortForward{
		ClusterID:  f.cluster,
		Target:     service.ForwardTarget{Kind: service.TargetPod, Namespace: "default", Name: "api-ready"},
		RemotePort: 8080,
		LocalPort:  freePort(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	if saved.ID == "" {
		t.Fatal("saved forward has no ID")
	}
	saved.RemotePort = 9000
	if _, err := f.svc.SaveForward(saved); err != nil {
		t.Fatal(err)
	}
	cfg, err := f.svc.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]service.PortForward{saved}, cfg.Forwards); diff != "" {
		t.Errorf("saved forwards mismatch (-want +got):\n%s", diff)
	}
	if _, err := f.svc.SaveForward(service.PortForward{ClusterID: "missing", Target: saved.Target, RemotePort: 1, LocalPort: 1}); err == nil {
		t.Error("saving a forward for an unknown cluster should fail")
	}

	started, err := f.svc.StartForward(context.Background(), saved)
	if err != nil {
		t.Fatal(err)
	}
	if started.ID != saved.ID {
		t.Errorf("started ID = %q, want saved ID %q", started.ID, saved.ID)
	}
	if again, err := f.svc.StartForward(context.Background(), saved); err != nil || again.ID != saved.ID {
		t.Errorf("starting a running saved forward again = %+v, %v; want the same forward and no error", again, err)
	}
	f.waitState(t, service.StateConnected)
	if diff := cmp.Diff([]string{"api-ready:9000", "ping"}, pingThroughPort(t, saved.LocalPort)); diff != "" {
		t.Errorf("bytes through local port mismatch (-want +got):\n%s", diff)
	}

	if err := f.svc.DeleteForward(saved.ID); err != nil {
		t.Fatal(err)
	}
	f.waitState(t, service.StateStopped)
	if got := f.svc.ForwardStatuses(); len(got) != 0 {
		t.Errorf("statuses after delete = %v, want none", got)
	}
	cfg, _ = f.svc.LoadConfig()
	if len(cfg.Forwards) != 0 {
		t.Errorf("forwards after delete = %v, want none", cfg.Forwards)
	}
	if err := f.svc.DeleteForward(saved.ID); err == nil {
		t.Error("deleting an unknown forward should fail")
	}
}

func TestForward_ReconnectsToNewPodWhenPodDies(t *testing.T) {
	now := time.Now()
	f := newForwardFixture(t,
		selectorService("default", "api", 80, intstr.FromInt32(8080)),
		pod("default", "api-0", "api", corev1.PodRunning, true, now),
	)
	ctx := context.Background()
	local := freePort(t)
	pf, err := f.svc.StartForward(ctx, service.PortForward{
		ClusterID:  f.cluster,
		Target:     service.ForwardTarget{Kind: service.TargetService, Namespace: "default", Name: "api"},
		RemotePort: 80,
		LocalPort:  local,
	})
	if err != nil {
		t.Fatal(err)
	}
	if ev := f.waitState(t, service.StateConnected); ev.Pod != "api-0" {
		t.Fatalf("resolved pod = %q, want api-0", ev.Pod)
	}
	if diff := cmp.Diff([]string{"api-0:8080", "ping"}, pingThroughPort(t, local)); diff != "" {
		t.Errorf("bytes through local port mismatch (-want +got):\n%s", diff)
	}

	if err := f.cs.CoreV1().Pods("default").Delete(ctx, "api-0", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.cs.CoreV1().Pods("default").Create(ctx, pod("default", "api-1", "api", corev1.PodRunning, true, now), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	f.api.killPod("api-0")

	if ev := f.waitState(t, service.StateReconnecting); ev.Error == "" {
		t.Error("reconnecting event carries no reason")
	}
	if ev := f.waitState(t, service.StateConnected); ev.Pod != "api-1" {
		t.Errorf("re-resolved pod = %q, want api-1", ev.Pod)
	}
	if diff := cmp.Diff([]string{"api-1:8080", "ping"}, pingThroughPort(t, local)); diff != "" {
		t.Errorf("bytes through local port mismatch (-want +got):\n%s", diff)
	}
	if err := f.svc.StopForward(pf.ID); err != nil {
		t.Fatal(err)
	}
	f.waitState(t, service.StateStopped)
}

func TestForward_ResumesAfterRouteReconnects(t *testing.T) {
	priv, pub := newKeyPair(t)
	f := newRouteFixture(t, writeKeyFile(t, priv, ""), pub)
	forwards := make(chan service.ForwardStatus, 100)
	emit := f.svc.Emit
	f.svc.Emit = func(name string, data any) {
		if status, ok := data.(service.ForwardStatus); ok {
			forwards <- status
			return
		}
		emit(name, data)
	}
	f.connect(t)

	local := freePort(t)
	pf, err := f.svc.StartForward(context.Background(), service.PortForward{
		ClusterID:  f.cluster,
		Target:     service.ForwardTarget{Kind: service.TargetPod, Namespace: "default", Name: "api-0"},
		RemotePort: 8080,
		LocalPort:  local,
	})
	if err != nil {
		t.Fatal(err)
	}
	waitForwardState(t, forwards, service.StateConnected)

	f.ssh.dropConnections()
	f.waitState(t, service.StateReconnecting)
	waitForwardState(t, forwards, service.StateReconnecting)
	f.waitState(t, service.StateConnected)
	waitForwardState(t, forwards, service.StateConnected)

	if diff := cmp.Diff([]string{"api-0:8080", "ping"}, pingThroughPort(t, local)); diff != "" {
		t.Errorf("bytes through local port mismatch (-want +got):\n%s", diff)
	}
	if err := f.svc.StopForward(pf.ID); err != nil {
		t.Fatal(err)
	}
}
