package service

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"path"
	"slices"
	"strconv"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"
	"k8s.io/streaming/pkg/httpstream"
	streamspdy "k8s.io/streaming/pkg/httpstream/spdy"
)

type TargetKind string

const (
	TargetService TargetKind = "service"
	TargetPod     TargetKind = "pod"
)

type ForwardTarget struct {
	Kind      TargetKind `yaml:"kind" json:"kind"`
	Namespace string     `yaml:"namespace" json:"namespace"`
	Name      string     `yaml:"name" json:"name"`
}

// PortForward binds a loopback port to one service or pod port; RemotePort is the service port for services.
type PortForward struct {
	ID         string        `yaml:"id" json:"id"`
	ClusterID  string        `yaml:"cluster" json:"clusterId"`
	Target     ForwardTarget `yaml:"target" json:"target"`
	RemotePort int           `yaml:"remotePort" json:"remotePort"`
	LocalPort  int           `yaml:"localPort" json:"localPort"`
}

// EventForwardState carries a ForwardStatus on every transition.
const EventForwardState = "forward:state"

type ForwardStatus struct {
	Forward PortForward `json:"forward"`
	// Pod is the backing pod the forward resolved to.
	Pod   string `json:"pod"`
	State State  `json:"state"`
	Error string `json:"error,omitempty"`
}

// PortInUseError reports a busy local port together with a free one to use instead.
type PortInUseError struct {
	Port      int `json:"port"`
	Suggested int `json:"suggested"`
}

func (e *PortInUseError) Error() string {
	return fmt.Sprintf("local port %d is in use, %d is free", e.Port, e.Suggested)
}

type forwardConn struct {
	cancel context.CancelFunc
	done   chan struct{}
	mu     sync.Mutex
	status ForwardStatus
}

// StartForward resolves the target pod, checks the local port and starts forwarding in the background.
// An empty ID gets a new one; a Saved Forward starts under its own ID, and starting it again while it runs is a no-op.
// State changes arrive as EventForwardState.
func (s *Service) StartForward(ctx context.Context, pf PortForward) (PortForward, error) {
	if err := validateForward(pf); err != nil {
		return PortForward{}, err
	}
	if pf.ID == "" {
		pf.ID = newID()
	}
	if err := checkLocalPort(pf.LocalPort); err != nil {
		return PortForward{}, err
	}
	runCtx, cancel := context.WithCancel(context.Background())
	fc := &forwardConn{cancel: cancel, done: make(chan struct{}), status: ForwardStatus{Forward: pf, State: StateIdle}}
	// Registered before resolving so a concurrent start of the same ID is a no-op rather than a second forward.
	s.mu.Lock()
	if s.forwards[pf.ID] != nil {
		s.mu.Unlock()
		cancel()
		return pf, nil
	}
	s.forwards[pf.ID] = fc
	s.mu.Unlock()
	dialer, podPort, err := s.resolveForward(ctx, runCtx, fc)
	if err != nil {
		s.mu.Lock()
		delete(s.forwards, pf.ID)
		s.mu.Unlock()
		cancel()
		close(fc.done)
		return PortForward{}, err
	}
	go s.runForward(runCtx, fc, dialer, podPort)
	return pf, nil
}

// SaveForward keeps a Port Forward as a Saved Forward under its ID, assigning one when empty and replacing an existing entry.
func (s *Service) SaveForward(pf PortForward) (PortForward, error) {
	if err := validateForward(pf); err != nil {
		return PortForward{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cfg, err := loadConfig(s.configPath)
	if err != nil {
		return PortForward{}, err
	}
	if _, err := findCluster(cfg, pf.ClusterID); err != nil {
		return PortForward{}, err
	}
	if pf.ID == "" {
		pf.ID = newID()
	}
	if i := findForward(cfg, pf.ID); i < 0 {
		cfg.Forwards = append(cfg.Forwards, pf)
	} else {
		cfg.Forwards[i] = pf
	}
	return pf, saveConfig(s.configPath, cfg)
}

// DeleteForward stops the forward if it is running and forgets the Saved Forward.
func (s *Service) DeleteForward(forwardID string) error {
	_ = s.StopForward(forwardID)
	s.mu.Lock()
	defer s.mu.Unlock()
	cfg, err := loadConfig(s.configPath)
	if err != nil {
		return err
	}
	i := findForward(cfg, forwardID)
	if i < 0 {
		return fmt.Errorf("unknown forward %q", forwardID)
	}
	cfg.Forwards = slices.Delete(cfg.Forwards, i, i+1)
	return saveConfig(s.configPath, cfg)
}

func findForward(cfg Config, forwardID string) int {
	return slices.IndexFunc(cfg.Forwards, func(f PortForward) bool { return f.ID == forwardID })
}

// StopForward closes the local port, returning once the forward is stopped and forgotten.
func (s *Service) StopForward(forwardID string) error {
	s.mu.Lock()
	fc := s.forwards[forwardID]
	delete(s.forwards, forwardID)
	s.mu.Unlock()
	if fc == nil {
		return nil
	}
	fc.cancel()
	<-fc.done
	return nil
}

// ForwardStatuses returns every forward of this session.
func (s *Service) ForwardStatuses() []ForwardStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]ForwardStatus, 0, len(s.forwards))
	for _, fc := range s.forwards {
		fc.mu.Lock()
		out = append(out, fc.status)
		fc.mu.Unlock()
	}
	slices.SortFunc(out, func(a, b ForwardStatus) int { return cmp.Compare(a.Forward.ID, b.Forward.ID) })
	return out
}

func validateForward(pf PortForward) error {
	switch {
	case pf.Target.Kind != TargetService && pf.Target.Kind != TargetPod:
		return fmt.Errorf("unknown target kind %q", pf.Target.Kind)
	case pf.Target.Namespace == "" || pf.Target.Name == "":
		return errors.New("target namespace and name are required")
	case pf.RemotePort < 1 || pf.RemotePort > 65535:
		return fmt.Errorf("remote port %d is out of range", pf.RemotePort)
	case pf.LocalPort < 1 || pf.LocalPort > 65535:
		return fmt.Errorf("local port %d is out of range", pf.LocalPort)
	}
	return nil
}

// checkLocalPort fails with a PortInUseError, carrying a free port, when the loopback port cannot be bound.
func checkLocalPort(port int) error {
	l, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err == nil {
		return l.Close()
	}
	free, ferr := net.Listen("tcp", "127.0.0.1:0")
	if ferr != nil {
		return err
	}
	defer func() { _ = free.Close() }()
	return &PortInUseError{Port: port, Suggested: free.Addr().(*net.TCPAddr).Port}
}

// resolvePod picks the backing pod and its container port the way kubectl port-forward does:
// a service's selector is matched, the best pod is a running, ready, least-restarted, oldest one,
// and the service port is translated through targetPort, by name if needed.
func resolvePod(ctx context.Context, k kube, target ForwardTarget, port int) (string, int, error) {
	if target.Kind == TargetPod {
		pod, err := k.client.CoreV1().Pods(target.Namespace).Get(ctx, target.Name, metav1.GetOptions{})
		if err != nil {
			return "", 0, err
		}
		if pod.Status.Phase != corev1.PodRunning {
			return "", 0, fmt.Errorf("pod %s is not running (%s)", pod.Name, pod.Status.Phase)
		}
		return target.Name, port, nil
	}
	svc, err := k.client.CoreV1().Services(target.Namespace).Get(ctx, target.Name, metav1.GetOptions{})
	if err != nil {
		return "", 0, err
	}
	if len(svc.Spec.Selector) == 0 {
		return "", 0, fmt.Errorf("service %s/%s has no selector", svc.Namespace, svc.Name)
	}
	i := slices.IndexFunc(svc.Spec.Ports, func(p corev1.ServicePort) bool { return int(p.Port) == port })
	if i < 0 {
		return "", 0, fmt.Errorf("service %s/%s has no port %d", svc.Namespace, svc.Name, port)
	}
	list, err := k.client.CoreV1().Pods(target.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labels.SelectorFromSet(svc.Spec.Selector).String(),
	})
	if err != nil {
		return "", 0, err
	}
	if len(list.Items) == 0 {
		return "", 0, fmt.Errorf("service %s/%s has no pods", svc.Namespace, svc.Name)
	}
	pod := slices.MinFunc(list.Items, podPreference)
	if pod.Status.Phase != corev1.PodRunning {
		return "", 0, fmt.Errorf("pod %s is not running (%s)", pod.Name, pod.Status.Phase)
	}
	targetPort := svc.Spec.Ports[i].TargetPort
	if targetPort.Type == intstr.String {
		for _, c := range pod.Spec.Containers {
			for _, p := range c.Ports {
				if p.Name == targetPort.StrVal {
					return pod.Name, int(p.ContainerPort), nil
				}
			}
		}
		return "", 0, fmt.Errorf("pod %s has no container port named %q", pod.Name, targetPort.StrVal)
	}
	if targetPort.IntVal == 0 {
		return pod.Name, port, nil
	}
	return pod.Name, int(targetPort.IntVal), nil
}

// podPreference orders pods as kubectl's attachable pod selection does; the minimum is the best target.
func podPreference(a, b corev1.Pod) int {
	return cmp.Or(
		cmp.Compare(boolRank(a.Status.Phase == corev1.PodRunning), boolRank(b.Status.Phase == corev1.PodRunning)),
		cmp.Compare(boolRank(podReady(a)), boolRank(podReady(b))),
		cmp.Compare(restarts(a), restarts(b)),
		a.CreationTimestamp.Compare(b.CreationTimestamp.Time),
	)
}

func boolRank(preferred bool) int {
	if preferred {
		return 0
	}
	return 1
}

func podReady(p corev1.Pod) bool {
	return slices.ContainsFunc(p.Status.Conditions, func(c corev1.PodCondition) bool {
		return c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue
	})
}

func restarts(p corev1.Pod) int32 {
	var n int32
	for _, c := range p.Status.ContainerStatuses {
		n += c.RestartCount
	}
	return n
}

// forwardDialer upgrades to SPDY through the config's own transport, so the Route's dial function is honoured
// where client-go's spdy.RoundTripperFor would dial the API server directly.
func forwardDialer(ctx context.Context, cfg *rest.Config, namespace, pod string) (httpstream.Dialer, error) {
	base, err := rest.TransportFor(cfg)
	if err != nil {
		return nil, err
	}
	upgrader, err := streamspdy.NewRoundTripperWithConfig(streamspdy.RoundTripperConfig{UpgradeTransport: base, PingPeriod: 5 * time.Second})
	if err != nil {
		return nil, err
	}
	rt, err := rest.HTTPWrappersForConfig(cfg, upgrader)
	if err != nil {
		return nil, err
	}
	u, _, err := rest.DefaultServerUrlFor(cfg)
	if err != nil {
		return nil, err
	}
	u.Path = path.Join(u.Path, "api/v1/namespaces", namespace, "pods", pod, "portforward")
	return &spdyDialer{ctx: ctx, upgrader: spdy.NewUpgraderForStreaming(upgrader), client: &http.Client{Transport: rt}, url: u.String()}, nil
}

// spdyDialer is client-go's SPDY dialer with the forward's context on the request, so Stop interrupts a stalled dial.
type spdyDialer struct {
	ctx      context.Context
	upgrader spdy.Upgrader
	client   *http.Client
	url      string
}

func (d *spdyDialer) Dial(protocols ...string) (httpstream.Connection, string, error) {
	req, err := http.NewRequestWithContext(d.ctx, http.MethodPost, d.url, nil)
	if err != nil {
		return nil, "", err
	}
	return spdy.NegotiateStreaming(d.upgrader, d.client, req, protocols...)
}

// resolveForward picks the backing pod afresh and builds a dialer to it. ctx bounds the lookup;
// dialCtx outlives it and bounds the forward's own dials.
func (s *Service) resolveForward(ctx, dialCtx context.Context, fc *forwardConn) (httpstream.Dialer, int, error) {
	pf := fc.status.Forward
	k, err := s.clusterClient(pf.ClusterID)
	if err != nil {
		return nil, 0, err
	}
	pod, podPort, err := resolvePod(ctx, k, pf.Target, pf.RemotePort)
	if err != nil {
		return nil, 0, err
	}
	dialer, err := forwardDialer(dialCtx, k.config, pf.Target.Namespace, pod)
	if err != nil {
		return nil, 0, err
	}
	fc.mu.Lock()
	fc.status.Pod = pod
	fc.mu.Unlock()
	return dialer, podPort, nil
}

// runForward keeps the local port open until ctx is cancelled: forward, wait for the drop, back off,
// re-resolve the pod, repeat. A Route that is down is polled every second so the forward resumes as soon as it is back.
func (s *Service) runForward(ctx context.Context, fc *forwardConn, dialer httpstream.Dialer, podPort int) {
	defer close(fc.done)
	backoff := time.Second
	s.setForwardState(fc, StateConnecting, nil)
	for {
		connected, err := s.forwardOnce(ctx, fc, dialer, podPort)
		if ctx.Err() != nil {
			s.setForwardState(fc, StateStopped, nil)
			return
		}
		if connected {
			backoff = time.Second
		}
		for {
			s.setForwardState(fc, StateReconnecting, err)
			wait := backoff
			if errors.Is(err, ErrRouteDown) {
				wait = time.Second
			} else {
				backoff = min(backoff*2, 30*time.Second)
			}
			select {
			case <-ctx.Done():
				s.setForwardState(fc, StateStopped, nil)
				return
			case <-time.After(wait):
			}
			dialer, podPort, err = s.resolveForward(ctx, ctx, fc)
			if err == nil {
				break
			}
			if ctx.Err() != nil {
				s.setForwardState(fc, StateStopped, nil)
				return
			}
		}
	}
}

// forwardOnce runs one port-forward session and reports whether it ever came up; a session that ends without
// an error lost its connection to the pod.
func (s *Service) forwardOnce(ctx context.Context, fc *forwardConn, dialer httpstream.Dialer, podPort int) (bool, error) {
	ready := make(chan struct{})
	ports := []string{strconv.Itoa(fc.status.Forward.LocalPort) + ":" + strconv.Itoa(podPort)}
	pf, err := portforward.NewOnAddressesForStreamingWithContext(ctx, dialer, []string{"127.0.0.1"}, ports, ready, nil, nil)
	if err != nil {
		return false, err
	}
	done := make(chan error, 1)
	go func() { done <- pf.ForwardPorts() }()
	connected := false
	select {
	case <-ready:
		connected = true
		s.setForwardState(fc, StateConnected, nil)
		err = <-done
	case err = <-done:
	}
	if err == nil {
		err = portforward.ErrLostConnectionToPod
	}
	return connected, err
}

// setForwardState emits only actual transitions, so a forward polling a stopped Route stays quiet.
func (s *Service) setForwardState(fc *forwardConn, state State, err error) {
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	fc.mu.Lock()
	changed := fc.status.State != state || fc.status.Error != msg
	fc.status.State = state
	fc.status.Error = msg
	status := fc.status
	fc.mu.Unlock()
	if changed {
		s.Emit(EventForwardState, status)
	}
}
