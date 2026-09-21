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
	"slices"
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
	"k8s.io/client-go/tools/remotecommand"
	"k8s.io/streaming/pkg/httpstream"
	"k8s.io/streaming/pkg/httpstream/spdy"
)

// testAPI serves the pod subresources: any pod is running, the log subresource streams seeded and live lines,
// and port-forward speaks SPDY where every data stream first receives "<pod>:<port>\n" and is then echoed.
type testAPI struct {
	mu    sync.Mutex
	conns map[string][]httpstream.Connection
	// logs and feeds are keyed by logKey: seeded backlog and live lines for the log subresource.
	logs   map[string][]string
	feeds  map[string]chan string
	gone   map[string]bool
	seeded int
	// shells are the commands the exec subresource can start; execs and resizes record what it was asked.
	shells  map[string]bool
	execs   []string
	resizes []remotecommand.TerminalSize
}

func newTestAPI() *testAPI {
	return &testAPI{conns: map[string][]httpstream.Connection{}, logs: map[string][]string{}, feeds: map[string]chan string{}, gone: map[string]bool{}}
}

func (a *testAPI) podHandler(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(r.URL.Path, "/")
	if len(parts) == 7 && parts[5] == "pods" && r.Method == http.MethodGet {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"apiVersion":"v1","kind":"Pod","metadata":{"namespace":%q,"name":%q},"spec":{"containers":[{"name":"app"}]},"status":{"phase":"Running"}}`, parts[4], parts[6])
		return
	}
	if len(parts) == 8 && parts[5] == "pods" && parts[7] == "log" {
		a.logHandler(w, r, parts[6])
		return
	}
	if len(parts) == 8 && parts[5] == "pods" && parts[7] == "exec" {
		a.execHandler(w, r)
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

// fixtureTimeout mirrors the request timeout the real client factory sets, short enough for a test to outlive it.
const fixtureTimeout = 300 * time.Millisecond

type forwardFixture struct {
	svc     *service.Service
	cs      *fake.Clientset
	api     *testAPI
	events  chan service.ForwardStatus
	cluster string
	path    string
	clients service.ClientFactory
}

// newForwardFixture lists with the fake clientset and forwards against an in-test API server speaking the port-forward protocol.
func newForwardFixture(t *testing.T, objects ...runtime.Object) *forwardFixture {
	t.Helper()
	f := &forwardFixture{cs: fake.NewClientset(objects...), api: newTestAPI(), events: make(chan service.ForwardStatus, 100), path: filepath.Join(t.TempDir(), "kubereach.yaml")}
	api := httptest.NewServer(http.HandlerFunc(f.api.podHandler))
	t.Cleanup(api.Close)
	f.clients = func(service.Cluster, service.DialFunc) (kubernetes.Interface, *rest.Config, error) {
		return f.cs, &rest.Config{Host: api.URL, Timeout: fixtureTimeout}, nil
	}
	f.svc = service.New(f.path, f.clients)
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
	return waitForward(t, events, func(ev service.ForwardStatus) bool {
		if ev.State == service.StateError && want != service.StateError {
			t.Fatalf("forward failed while waiting for %q: %s", want, ev.Error)
		}
		return ev.State == want
	})
}

func waitForward(t *testing.T, events <-chan service.ForwardStatus, ok func(service.ForwardStatus) bool) service.ForwardStatus {
	t.Helper()
	deadline := time.After(10 * time.Second)
	var last service.ForwardStatus
	for {
		select {
		case last = <-events:
			if ok(last) {
				return last
			}
		case <-deadline:
			t.Fatalf("timed out waiting for forward status; last %+v", last)
		}
	}
}

// save persists an enabled forward to the target; a zero local port is assigned by the service.
func (f *forwardFixture) save(t *testing.T, kind service.TargetKind, name string, remotePort, localPort int) service.PortForward {
	t.Helper()
	pf, err := f.svc.SaveForward(service.PortForward{
		ClusterID:  f.cluster,
		Target:     service.ForwardTarget{Kind: kind, Namespace: "default", Name: name},
		RemotePort: remotePort,
		LocalPort:  localPort,
		Enabled:    true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return pf
}

// touchPort opens and closes one connection to the local port, enough to trigger the first-use dial.
func touchPort(t *testing.T, port int) {
	t.Helper()
	conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
}

func dialFails(port int) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second)
	if err == nil {
		_ = conn.Close()
	}
	return err != nil
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
	local := freePort(t)
	pf := f.save(t, service.TargetService, "api", 80, local)
	if pf.ID == "" || pf.LocalPort != local {
		t.Fatalf("saved forward = %+v, want an ID and local port %d", pf, local)
	}
	if ev := f.waitState(t, service.StateIdle); ev.Forward != pf {
		t.Errorf("idle event forward = %+v, want %+v", ev.Forward, pf)
	}
	f.api.mu.Lock()
	dialed := len(f.api.conns)
	f.api.mu.Unlock()
	if dialed != 0 {
		t.Errorf("%d pods dialed before any connection, want none", dialed)
	}

	if diff := cmp.Diff([]string{"api-ready:8080", "ping"}, pingThroughPort(t, local)); diff != "" {
		t.Errorf("bytes through local port mismatch (-want +got):\n%s", diff)
	}
	if ev := f.waitState(t, service.StateConnected); ev.Pod != "api-ready" {
		t.Errorf("resolved pod = %q, want api-ready", ev.Pod)
	}
	want := []service.ForwardStatus{{Forward: pf, Pod: "api-ready", State: service.StateConnected}}
	if diff := cmp.Diff(want, f.svc.ForwardStatuses()); diff != "" {
		t.Errorf("statuses mismatch (-want +got):\n%s", diff)
	}

	if err := f.svc.SetForwardEnabled(pf.ID, false); err != nil {
		t.Fatal(err)
	}
	f.waitState(t, service.StateStopped)
	if got := f.svc.ForwardStatuses(); len(got) != 0 {
		t.Errorf("statuses after switching off = %v, want none", got)
	}
	if !dialFails(local) {
		t.Error("local port still accepts connections after switching off")
	}
	cfg, _ := f.svc.LoadConfig()
	if len(cfg.Forwards) != 1 || cfg.Forwards[0].Enabled {
		t.Errorf("saved forwards after switching off = %+v, want one, off", cfg.Forwards)
	}
}

func TestForward_ToPodUsesThatPod(t *testing.T) {
	f := newForwardFixture(t, pod("default", "api-ready", "api", corev1.PodRunning, true, time.Now()))
	pf := f.save(t, service.TargetPod, "api-ready", 9000, 0)
	if diff := cmp.Diff([]string{"api-ready:9000", "ping"}, pingThroughPort(t, pf.LocalPort)); diff != "" {
		t.Errorf("bytes through local port mismatch (-want +got):\n%s", diff)
	}
}

func TestForward_AutoPortSkipsHeldAndBoundPortsAcrossRestarts(t *testing.T) {
	f := newForwardFixture(t, pod("default", "api-ready", "api", corev1.PodRunning, true, time.Now()))
	busy, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", service.ForwardPortStart))
	for p := service.ForwardPortStart + 1; err != nil && p < service.ForwardPortStart+100; p++ {
		busy, err = net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p))
	}
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = busy.Close() }()
	busyPort := busy.Addr().(*net.TCPAddr).Port

	first := f.save(t, service.TargetPod, "api-ready", 8080, 0)
	second := f.save(t, service.TargetPod, "api-ready", 8081, 0)
	if first.LocalPort < service.ForwardPortStart || first.LocalPort == busyPort || second.LocalPort <= first.LocalPort {
		t.Errorf("assigned ports %d and %d, want distinct ports from %d skipping busy %d", first.LocalPort, second.LocalPort, service.ForwardPortStart, busyPort)
	}
	// An off forward still holds its port.
	if err := f.svc.SetForwardEnabled(first.ID, false); err != nil {
		t.Fatal(err)
	}
	restarted := service.New(f.path, f.clients)
	third, err := restarted.SaveForward(service.PortForward{ClusterID: f.cluster, Target: first.Target, RemotePort: 8082})
	if err != nil {
		t.Fatal(err)
	}
	if third.LocalPort <= second.LocalPort {
		t.Errorf("port after restart = %d, want one above %d and %d", third.LocalPort, first.LocalPort, second.LocalPort)
	}
	// Deleting frees the port for the next assignment.
	if err := f.svc.DeleteForward(first.ID); err != nil {
		t.Fatal(err)
	}
	fourth, err := restarted.SaveForward(service.PortForward{ClusterID: f.cluster, Target: first.Target, RemotePort: 8083})
	if err != nil {
		t.Fatal(err)
	}
	if fourth.LocalPort != first.LocalPort {
		t.Errorf("port after delete = %d, want the freed %d", fourth.LocalPort, first.LocalPort)
	}
}

func TestForward_EnabledForwardsAreBoundAtLaunch(t *testing.T) {
	f := newForwardFixture(t, pod("default", "api-ready", "api", corev1.PodRunning, true, time.Now()))
	on := f.save(t, service.TargetPod, "api-ready", 8080, 0)
	off := f.save(t, service.TargetPod, "api-ready", 8081, 0)
	if err := f.svc.SetForwardEnabled(off.ID, false); err != nil {
		t.Fatal(err)
	}
	f.svc.Shutdown()
	if !dialFails(on.LocalPort) {
		t.Fatal("port still bound after shutdown")
	}

	restarted := service.New(f.path, f.clients)
	restarted.Emit = f.svc.Emit
	if err := restarted.BindForwards(); err != nil {
		t.Fatal(err)
	}
	statuses := restarted.ForwardStatuses()
	if len(statuses) != 1 || statuses[0].Forward.ID != on.ID || statuses[0].State != service.StateIdle {
		t.Fatalf("statuses after restart = %+v, want the enabled forward idle", statuses)
	}
	if diff := cmp.Diff([]string{"api-ready:8080", "ping"}, pingThroughPort(t, on.LocalPort)); diff != "" {
		t.Errorf("bytes through local port mismatch (-want +got):\n%s", diff)
	}
	if !dialFails(off.LocalPort) {
		t.Error("an off forward's port accepts connections")
	}
	restarted.Shutdown()
}

func TestForward_DuplicateTargetIsRefused(t *testing.T) {
	f := newForwardFixture(t, pod("default", "api-ready", "api", corev1.PodRunning, true, time.Now()))
	first := f.save(t, service.TargetPod, "api-ready", 8080, 0)
	again, err := f.svc.SaveForward(service.PortForward{ClusterID: f.cluster, Target: first.Target, RemotePort: 8080})
	var exists *service.ForwardExistsError
	if !errors.As(err, &exists) || exists.Forward != first || again != first {
		t.Fatalf("saving the same target again = %+v, %v; want ForwardExistsError with %+v", again, err, first)
	}
	// Editing the forward itself is not a duplicate.
	first.LocalPort = freePort(t)
	if _, err := f.svc.SaveForward(first); err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]string{"api-ready:8080", "ping"}, pingThroughPort(t, first.LocalPort)); diff != "" {
		t.Errorf("bytes through edited local port mismatch (-want +got):\n%s", diff)
	}
	cfg, _ := f.svc.LoadConfig()
	if diff := cmp.Diff([]service.PortForward{first}, cfg.Forwards); diff != "" {
		t.Errorf("saved forwards mismatch (-want +got):\n%s", diff)
	}
	if _, err := f.svc.SaveForward(service.PortForward{ClusterID: "missing", Target: first.Target, RemotePort: 1}); err == nil {
		t.Error("saving a forward for an unknown cluster should fail")
	}
	if err := f.svc.DeleteForward(first.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.DeleteForward(first.ID); err == nil {
		t.Error("deleting an unknown forward should fail")
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

	_, err = f.svc.SaveForward(service.PortForward{
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
	if got, _ := f.svc.LoadConfig(); len(got.Forwards) != 0 {
		t.Errorf("a refused forward was saved: %+v", got.Forwards)
	}

	// A saved port that is busy at launch is reported on the row, never reassigned.
	pf := f.save(t, service.TargetPod, "api-ready", 8080, 0)
	f.svc.Shutdown()
	taken, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", pf.LocalPort))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = taken.Close() }()
	if err := f.svc.BindForwards(); err != nil {
		t.Fatal(err)
	}
	if ev := f.waitState(t, service.StateError); ev.Forward.LocalPort != pf.LocalPort || ev.Error == "" {
		t.Errorf("row with busy port = %+v, want same port and an error", ev)
	}
	if err := f.svc.SetForwardEnabled(pf.ID, true); !errors.As(err, &inUse) {
		t.Errorf("switching on with the port still busy = %v, want PortInUseError", err)
	}
	_ = taken.Close()
	if err := f.svc.SetForwardEnabled(pf.ID, true); err != nil {
		t.Fatal(err)
	}
	f.waitState(t, service.StateIdle)
	if diff := cmp.Diff([]string{"api-ready:8080", "ping"}, pingThroughPort(t, pf.LocalPort)); diff != "" {
		t.Errorf("bytes once the port is free mismatch (-want +got):\n%s", diff)
	}
}

func TestForward_LazyConnectionDropsAfterIdleAndFollowsReplacedPod(t *testing.T) {
	now := time.Now()
	f := newForwardFixture(t,
		selectorService("default", "api", 80, intstr.FromInt32(8080)),
		pod("default", "api-0", "api", corev1.PodRunning, true, now),
	)
	f.svc.ForwardIdle = 200 * time.Millisecond
	ctx := context.Background()
	pf := f.save(t, service.TargetService, "api", 80, 0)

	if diff := cmp.Diff([]string{"api-0:8080", "ping"}, pingThroughPort(t, pf.LocalPort)); diff != "" {
		t.Errorf("bytes through local port mismatch (-want +got):\n%s", diff)
	}
	if ev := f.waitState(t, service.StateConnected); ev.Pod != "api-0" {
		t.Errorf("resolved pod = %q, want api-0", ev.Pod)
	}
	// The pod connection outlives the idle period while a local connection is open, and is dropped once none is.
	held, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", pf.LocalPort))
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(3 * f.svc.ForwardIdle)
	f.api.mu.Lock()
	conns := slices.Clone(f.api.conns["api-0"])
	f.api.mu.Unlock()
	if len(conns) != 1 {
		t.Fatalf("connections to api-0 = %d, want 1", len(conns))
	}
	select {
	case <-conns[0].CloseChan():
		t.Fatal("pod connection dropped while a local connection was open")
	default:
	}
	_ = held.Close()
	f.waitState(t, service.StateIdle)
	select {
	case <-conns[0].CloseChan():
	case <-time.After(5 * time.Second):
		t.Fatal("pod connection still open after idle period")
	}

	// The pod is replaced while idle: the next connection resolves the new one.
	if err := f.cs.CoreV1().Pods("default").Delete(ctx, "api-0", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.cs.CoreV1().Pods("default").Create(ctx, pod("default", "api-1", "api", corev1.PodRunning, true, now), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]string{"api-1:8080", "ping"}, pingThroughPort(t, pf.LocalPort)); diff != "" {
		t.Errorf("bytes after pod replacement mismatch (-want +got):\n%s", diff)
	}
	if ev := f.waitState(t, service.StateConnected); ev.Pod != "api-1" {
		t.Errorf("re-resolved pod = %q, want api-1", ev.Pod)
	}
	// A pod killed under an open connection returns the forward to idle; the port stays bound.
	f.api.killPod("api-1")
	f.waitState(t, service.StateIdle)
	if dialFails(pf.LocalPort) {
		t.Error("local port closed after the pod went away")
	}
}

func TestForward_FirstUseWithoutRunningPodReportsErrorAndRetries(t *testing.T) {
	f := newForwardFixture(t,
		selectorService("default", "api", 80, intstr.FromInt32(8080)),
		pod("default", "api-pending", "api", corev1.PodPending, false, time.Now()),
	)
	pf := f.save(t, service.TargetService, "api", 80, 0)
	touchPort(t, pf.LocalPort)
	if ev := f.waitState(t, service.StateError); !strings.Contains(ev.Error, "not running") {
		t.Errorf("error row = %+v, want the pending pod reported", ev)
	}
	if _, err := f.cs.CoreV1().Pods("default").Create(context.Background(), pod("default", "api-ready", "api", corev1.PodRunning, true, time.Now()), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]string{"api-ready:8080", "ping"}, pingThroughPort(t, pf.LocalPort)); diff != "" {
		t.Errorf("bytes once a pod runs mismatch (-want +got):\n%s", diff)
	}
	f.waitState(t, service.StateConnected)
}

// captureForwards diverts forward events into a channel, leaving Route events on the fixture's own.
func (f *routeFixture) captureForwards() <-chan service.ForwardStatus {
	forwards := make(chan service.ForwardStatus, 100)
	emit := f.svc.Emit
	f.svc.Emit = func(name string, data any) {
		if status, ok := data.(service.ForwardStatus); ok {
			forwards <- status
			return
		}
		emit(name, data)
	}
	return forwards
}

// saveForward persists an enabled forward to api-0 on the Route fixture's Cluster.
func (f *routeFixture) saveForward(t *testing.T) service.PortForward {
	t.Helper()
	pf, err := f.svc.SaveForward(service.PortForward{
		ClusterID:  f.cluster,
		Target:     service.ForwardTarget{Kind: service.TargetPod, Namespace: "default", Name: "api-0"},
		RemotePort: 8080,
		Enabled:    true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return pf
}

func TestForward_ThroughRouteAndDownRoute(t *testing.T) {
	priv, pub := newKeyPair(t)
	f := newRouteFixture(t, writeKeyFile(t, priv, ""), pub)
	forwards := f.captureForwards()
	pf := f.saveForward(t)

	// The Route is not up yet: the dial fails on the row and the port stays bound.
	touchPort(t, pf.LocalPort)
	if ev := waitForwardState(t, forwards, service.StateError); !strings.Contains(ev.Error, service.ErrRouteDown.Error()) {
		t.Errorf("row with Route down = %+v, want ErrRouteDown", ev)
	}

	f.connect(t)
	if diff := cmp.Diff([]string{"api-0:8080", "ping"}, pingThroughPort(t, pf.LocalPort)); diff != "" {
		t.Errorf("bytes through local port mismatch (-want +got):\n%s", diff)
	}
	waitForwardState(t, forwards, service.StateConnected)
	apiAddr := strings.TrimPrefix(f.api.URL, "http://")
	if got := f.ssh.forwardedTo(); len(got) == 0 || got[0] != apiAddr {
		t.Errorf("SSH server forwarded to %v, want [%s]", got, apiAddr)
	}

	// A network blip drops the pod connection; the port survives and works again once the Route is back.
	f.ssh.dropConnections()
	f.waitState(t, service.StateReconnecting)
	waitForwardState(t, forwards, service.StateIdle)
	f.waitState(t, service.StateConnected)
	if diff := cmp.Diff([]string{"api-0:8080", "ping"}, pingThroughPort(t, pf.LocalPort)); diff != "" {
		t.Errorf("bytes after Route reconnect mismatch (-want +got):\n%s", diff)
	}
}
