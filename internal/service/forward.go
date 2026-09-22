package service

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
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
// A zero LocalPort is assigned by SaveForward; Enabled is whether the port is bound whenever Kubereach runs.
type PortForward struct {
	ID         string        `yaml:"id" json:"id"`
	ClusterID  string        `yaml:"cluster" json:"clusterId"`
	Target     ForwardTarget `yaml:"target" json:"target"`
	RemotePort int           `yaml:"remotePort" json:"remotePort"`
	LocalPort  int           `yaml:"localPort" json:"localPort"`
	Enabled    bool          `yaml:"enabled" json:"enabled"`
}

// sameTarget reports whether two forwards point at the same port of the same target in the same Cluster.
func (pf PortForward) sameTarget(o PortForward) bool {
	return pf.ClusterID == o.ClusterID && pf.Target == o.Target && pf.RemotePort == o.RemotePort
}

// ForwardPortStart is the first local port handed out to a forward saved without one.
const ForwardPortStart = 20000

// EventForwardState carries a ForwardStatus on every transition.
const EventForwardState = "forward:state"

// ForwardStatus describes a bound forward: idle until the first inbound connection, connected while a pod
// connection is up, error when the port could not be bound or the last dial failed.
type ForwardStatus struct {
	Forward PortForward `json:"forward"`
	// Pod is the backing pod while a connection is up.
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

// ForwardExistsError reports that the target is already a Saved Forward.
type ForwardExistsError struct {
	Forward PortForward `json:"forward"`
}

func (e *ForwardExistsError) Error() string {
	pf := e.Forward
	return fmt.Sprintf("%s/%s:%d is already saved on localhost:%d", pf.Target.Namespace, pf.Target.Name, pf.RemotePort, pf.LocalPort)
}

// forwardConn owns one bound local port. The pod connection is dialed on the first inbound connection and
// dropped once no local connection has used it for ForwardIdle.
type forwardConn struct {
	cancel context.CancelFunc
	ln     net.Listener
	// ponytail: dial serialises pod dials, so connections arriving during one queue rather than dial in parallel.
	dial    sync.Mutex
	mu      sync.Mutex
	status  ForwardStatus
	closed  bool
	conn    httpstream.Connection
	podPort int
	active  int
	idle    *time.Timer
	nextID  int
}

// SaveForward persists a Saved Forward, assigning an ID when empty and the lowest free port from ForwardPortStart
// when LocalPort is zero, then binds or releases its port to match Enabled. A new forward to a target that is
// already saved is refused with a ForwardExistsError carrying the existing one.
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
	if i := slices.IndexFunc(cfg.Forwards, func(f PortForward) bool { return f.ID != pf.ID && f.sameTarget(pf) }); i >= 0 {
		return cfg.Forwards[i], &ForwardExistsError{Forward: cfg.Forwards[i]}
	}
	if pf.ID == "" {
		pf.ID = newID()
	}
	i := findForward(cfg, pf.ID)
	switch {
	case pf.LocalPort == 0:
		if pf.LocalPort, err = nextFreePort(cfg); err != nil {
			return PortForward{}, err
		}
	case i >= 0 && cfg.Forwards[i].LocalPort == pf.LocalPort:
		// The port is ours already, bound or not.
	default:
		if err := checkLocalPort(pf.LocalPort); err != nil {
			return PortForward{}, err
		}
	}
	if i < 0 {
		cfg.Forwards = append(cfg.Forwards, pf)
	} else {
		cfg.Forwards[i] = pf
	}
	if err := s.saveConfig(cfg); err != nil {
		return PortForward{}, err
	}
	s.applyForward(pf)
	return pf, nil
}

// nextFreePort is the lowest port from ForwardPortStart that no Saved Forward holds and that is not bound locally.
func nextFreePort(cfg Config) (int, error) {
	held := map[int]bool{}
	for _, f := range cfg.Forwards {
		held[f.LocalPort] = true
	}
	for port := ForwardPortStart; port <= 65535; port++ {
		if !held[port] && portFree(port) {
			return port, nil
		}
	}
	return 0, errors.New("no free local port left")
}

// SetForwardEnabled switches a Saved Forward on or off, binding or releasing its port.
func (s *Service) SetForwardEnabled(forwardID string, enabled bool) error {
	return s.editForwards(func(cfg *Config) ([]PortForward, error) {
		i := findForward(*cfg, forwardID)
		if i < 0 {
			return nil, fmt.Errorf("unknown forward %q", forwardID)
		}
		if fc := s.forwards[forwardID]; enabled && (fc == nil || fc.ln == nil) {
			if err := checkLocalPort(cfg.Forwards[i].LocalPort); err != nil {
				return nil, err
			}
		}
		cfg.Forwards[i].Enabled = enabled
		return cfg.Forwards[i : i+1], nil
	})
}

// BindForwards binds every Saved Forward that is on; called once at launch.
func (s *Service) BindForwards() error {
	return s.editForwards(func(cfg *Config) ([]PortForward, error) { return cfg.Forwards, nil })
}

// DeleteForward releases the port if it is bound and forgets the Saved Forward.
func (s *Service) DeleteForward(forwardID string) error {
	return s.editForwards(func(cfg *Config) ([]PortForward, error) {
		i := findForward(*cfg, forwardID)
		if i < 0 {
			return nil, fmt.Errorf("unknown forward %q", forwardID)
		}
		gone := cfg.Forwards[i]
		gone.Enabled = false
		cfg.Forwards = slices.Delete(cfg.Forwards, i, i+1)
		return []PortForward{gone}, nil
	})
}

// editForwards lets edit change the Saved Forwards, persists the result and binds or releases the forwards edit
// returns so they match their Enabled.
func (s *Service) editForwards(edit func(cfg *Config) ([]PortForward, error)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cfg, err := loadConfig(s.configPath)
	if err != nil {
		return err
	}
	apply, err := edit(&cfg)
	if err != nil {
		return err
	}
	if err := s.saveConfig(cfg); err != nil {
		return err
	}
	for _, pf := range apply {
		s.applyForward(pf)
	}
	return nil
}

func findForward(cfg Config, forwardID string) int {
	return slices.IndexFunc(cfg.Forwards, func(f PortForward) bool { return f.ID == forwardID })
}

// ForwardStatuses returns every bound forward.
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

// applyForward brings the bound port in line with the saved definition; a forward whose port could not be
// bound is tried again. Callers hold s.mu.
func (s *Service) applyForward(pf PortForward) {
	fc := s.forwards[pf.ID]
	if fc != nil && (!pf.Enabled || fc.status.Forward != pf || fc.ln == nil) {
		s.unbindForward(fc)
		fc = nil
	}
	if pf.Enabled && fc == nil {
		s.bindForward(pf)
	}
}

// bindForward opens the loopback listener; a port that cannot be bound leaves the forward registered in error
// so its row stays visible. Callers hold s.mu.
func (s *Service) bindForward(pf PortForward) {
	ctx, cancel := context.WithCancel(context.Background())
	fc := &forwardConn{cancel: cancel, status: ForwardStatus{Forward: pf, State: StateIdle}}
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(pf.LocalPort)))
	if err != nil {
		fc.status.State, fc.status.Error = StateError, err.Error()
	} else {
		fc.ln = ln
		go s.acceptForward(ctx, fc)
	}
	s.forwards[pf.ID] = fc
	s.Emit(EventForwardState, fc.status)
}

// unbindForward closes the listener and any pod connection; callers hold s.mu.
func (s *Service) unbindForward(fc *forwardConn) {
	delete(s.forwards, fc.status.Forward.ID)
	fc.cancel()
	fc.mu.Lock()
	if fc.ln != nil {
		_ = fc.ln.Close()
	}
	if fc.idle != nil {
		fc.idle.Stop()
	}
	if fc.conn != nil {
		_ = fc.conn.Close()
		fc.conn = nil
	}
	fc.status.Pod, fc.status.State, fc.status.Error = "", StateStopped, ""
	fc.closed = true
	status := fc.status
	fc.mu.Unlock()
	s.Emit(EventForwardState, status)
}

func validateForward(pf PortForward) error {
	switch {
	case pf.Target.Kind != TargetService && pf.Target.Kind != TargetPod:
		return fmt.Errorf("unknown target kind %q", pf.Target.Kind)
	case pf.Target.Namespace == "" || pf.Target.Name == "":
		return errors.New("target namespace and name are required")
	case pf.RemotePort < 1 || pf.RemotePort > 65535:
		return fmt.Errorf("remote port %d is out of range", pf.RemotePort)
	case pf.LocalPort < 0 || pf.LocalPort > 65535:
		return fmt.Errorf("local port %d is out of range", pf.LocalPort)
	}
	return nil
}

func portFree(port int) bool {
	l, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return false
	}
	_ = l.Close()
	return true
}

// checkLocalPort fails with a PortInUseError, carrying a free port, when the loopback port cannot be bound.
func checkLocalPort(port int) error {
	if portFree(port) {
		return nil
	}
	free, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
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

// forwardDialer dials the pod's port-forward subresource over SPDY through the Route.
func forwardDialer(ctx context.Context, cfg *rest.Config, namespace, pod string) (httpstream.Dialer, error) {
	rt, upgrader, err := spdyTransport(cfg)
	if err != nil {
		return nil, err
	}
	u, _, err := rest.DefaultServerUrlFor(cfg)
	if err != nil {
		return nil, err
	}
	u.Path = path.Join(u.Path, "api/v1/namespaces", namespace, "pods", pod, "portforward")
	return &spdyDialer{ctx: ctx, upgrader: upgrader, client: &http.Client{Transport: rt}, url: u.String()}, nil
}

// spdyDialer is client-go's SPDY dialer with the forward's context on the request, so Stop interrupts a stalled connect.
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

func (s *Service) acceptForward(ctx context.Context, fc *forwardConn) {
	for {
		c, err := fc.ln.Accept()
		if err != nil {
			return
		}
		go s.serveForward(ctx, fc, c)
	}
}

// serveForward carries one local connection over a data stream of the pod connection, the way client-go's
// port-forward does; the paired error stream reports a pod-side failure such as a refused port.
func (s *Service) serveForward(ctx context.Context, fc *forwardConn, c net.Conn) {
	defer func() { _ = c.Close() }()
	conn, podPort, id, err := s.forwardConnection(ctx, fc)
	if err != nil {
		return
	}
	defer s.releaseForward(fc)
	headers := http.Header{}
	headers.Set(corev1.StreamType, corev1.StreamTypeError)
	headers.Set(corev1.PortHeader, strconv.Itoa(podPort))
	headers.Set(corev1.PortForwardRequestIDHeader, strconv.Itoa(id))
	errStream, err := conn.CreateStream(headers)
	if err != nil {
		return
	}
	_ = errStream.Close()
	defer conn.RemoveStreams(errStream)
	defer func() { _ = errStream.Reset() }()
	headers.Set(corev1.StreamType, corev1.StreamTypeData)
	data, err := conn.CreateStream(headers)
	if err != nil {
		return
	}
	defer conn.RemoveStreams(data)
	go func() {
		if msg, _ := io.ReadAll(errStream); len(msg) > 0 {
			s.updateForward(fc, func(st *ForwardStatus) { st.Error = string(msg) })
		}
	}()
	remoteDone := make(chan struct{})
	go func() {
		_, _ = io.Copy(c, data)
		close(remoteDone)
	}()
	go func() {
		_, _ = io.Copy(data, c)
		_ = data.Close()
	}()
	select {
	case <-remoteDone:
	case <-ctx.Done():
	}
}

// forwardConnection returns the pod connection, dialing a ready pod when there is none, and counts the caller
// as active on it until releaseForward. A dial that fails puts the row in error; the next connection tries again.
func (s *Service) forwardConnection(ctx context.Context, fc *forwardConn) (httpstream.Connection, int, int, error) {
	fc.dial.Lock()
	defer fc.dial.Unlock()
	// attach counts the caller on the current connection; under fc.mu, so the idle timer cannot close it in between.
	attach := func() (int, int) {
		if fc.idle != nil {
			fc.idle.Stop()
		}
		fc.active++
		fc.nextID++
		return fc.podPort, fc.nextID
	}
	fc.mu.Lock()
	if conn := fc.conn; conn != nil {
		select {
		case <-conn.CloseChan():
		default:
			podPort, id := attach()
			fc.mu.Unlock()
			return conn, podPort, id, nil
		}
	}
	fc.mu.Unlock()
	pod, podPort, conn, err := s.dialForwardBounded(ctx, fc.status.Forward)
	if err != nil {
		s.updateForward(fc, func(st *ForwardStatus) { st.Pod, st.State, st.Error = "", StateError, err.Error() })
		return nil, 0, 0, err
	}
	fc.mu.Lock()
	if fc.closed {
		fc.mu.Unlock()
		_ = conn.Close()
		return nil, 0, 0, errors.New("forward was turned off")
	}
	fc.conn, fc.podPort = conn, podPort
	_, id := attach()
	fc.mu.Unlock()
	go func() {
		<-conn.CloseChan()
		s.dropForward(fc, conn)
	}()
	s.updateForward(fc, func(st *ForwardStatus) { st.Pod, st.State, st.Error = pod, StateConnected, "" })
	return conn, podPort, id, nil
}

const forwardDialTimeout = 30 * time.Second

// dialForwardBounded gives up on the dial without waiting for it: the SPDY upgrade reads its response
// without watching the context.
func (s *Service) dialForwardBounded(ctx context.Context, pf PortForward) (string, int, httpstream.Connection, error) {
	ctx, cancel := context.WithTimeout(ctx, forwardDialTimeout)
	defer cancel()
	type dialed struct {
		pod     string
		podPort int
		conn    httpstream.Connection
		err     error
	}
	result := make(chan dialed)
	go func() {
		var d dialed
		d.pod, d.podPort, d.conn, d.err = s.dialForward(ctx, pf)
		select {
		case result <- d:
		case <-ctx.Done():
			if d.conn != nil {
				_ = d.conn.Close()
			}
		}
	}()
	select {
	case d := <-result:
		return d.pod, d.podPort, d.conn, d.err
	case <-ctx.Done():
		return "", 0, nil, fmt.Errorf("dial pod: %w", ctx.Err())
	}
}

// dialForward resolves the backing pod the way kubectl does and opens a port-forward connection to it through the Route.
func (s *Service) dialForward(ctx context.Context, pf PortForward) (string, int, httpstream.Connection, error) {
	k, err := s.clusterClient(pf.ClusterID)
	if err != nil {
		return "", 0, nil, err
	}
	pod, podPort, err := resolvePod(ctx, k, pf.Target, pf.RemotePort)
	if err != nil {
		return "", 0, nil, err
	}
	dialer, err := forwardDialer(ctx, k.config, pf.Target.Namespace, pod)
	if err != nil {
		return "", 0, nil, err
	}
	conn, _, err := dialer.Dial(portforward.PortForwardProtocolV1Name)
	if err != nil {
		return "", 0, nil, err
	}
	return pod, podPort, conn, nil
}

// releaseForward arms the idle timer once the last local connection is gone.
func (s *Service) releaseForward(fc *forwardConn) {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	if fc.active--; fc.active > 0 {
		return
	}
	fc.idle = time.AfterFunc(s.ForwardIdle, func() {
		fc.mu.Lock()
		defer fc.mu.Unlock()
		if fc.active == 0 && fc.conn != nil {
			_ = fc.conn.Close()
		}
	})
}

// dropForward forgets a closed pod connection, whether the idle timer, the pod or the Route ended it; the next
// inbound connection dials afresh.
func (s *Service) dropForward(fc *forwardConn, conn httpstream.Connection) {
	fc.mu.Lock()
	dropped := fc.conn == conn
	if dropped {
		fc.conn = nil
	}
	fc.mu.Unlock()
	if dropped {
		s.updateForward(fc, func(st *ForwardStatus) { st.Pod, st.State, st.Error = "", StateIdle, "" })
	}
}

// updateForward emits the changed status; a forward that was unbound stays stopped whatever its goroutines report late.
func (s *Service) updateForward(fc *forwardConn, update func(*ForwardStatus)) {
	fc.mu.Lock()
	if fc.closed {
		fc.mu.Unlock()
		return
	}
	update(&fc.status)
	status := fc.status
	fc.mu.Unlock()
	s.Emit(EventForwardState, status)
}
