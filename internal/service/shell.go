package service

import (
	"cmp"
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strings"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
	"k8s.io/client-go/transport/spdy"
	"k8s.io/client-go/util/exec"
	streamspdy "k8s.io/streaming/pkg/httpstream/spdy"
)

// ShellTarget is the container an interactive shell is opened in; an empty Container means the pod's first one.
type ShellTarget struct {
	ClusterID string `json:"clusterId"`
	Namespace string `json:"namespace"`
	Pod       string `json:"pod"`
	Container string `json:"container"`
}

// EventShellOutput carries a ShellOutput per chunk read from the terminal.
const EventShellOutput = "shell:output"

type ShellOutput struct {
	SessionID string `json:"sessionId"`
	Data      []byte `json:"data"`
}

// EventShellState carries a ShellStatus on every transition; a session that ended on its own is forgotten as it does.
const EventShellState = "shell:state"

type ShellStatus struct {
	ID     string      `json:"id"`
	Target ShellTarget `json:"target"`
	// Shell is the command currently being tried, then the one that started.
	Shell string `json:"shell"`
	State State  `json:"state"`
	Error string `json:"error,omitempty"`
}

// shells are tried in order until one starts, so minimal images still open.
var shells = []string{"bash", "zsh", "sh"}

type shellConn struct {
	cancel context.CancelFunc
	done   chan struct{}
	mu     sync.Mutex
	status ShellStatus
	size   remotecommand.TerminalSize
	// attempt is the exec currently attached, nil between shells and after the last one.
	attempt *shellAttempt
}

// shellAttempt is one exec's stdin and resize queue; each shell tried gets its own so a dead attempt's readers never steal input.
type shellAttempt struct {
	stdin *io.PipeWriter
	sizes chan remotecommand.TerminalSize
}

// StartShell opens a TTY exec session in the container and returns once the pod is known; output arrives as
// EventShellOutput and transitions as EventShellState. cols and rows are the terminal's initial size.
func (s *Service) StartShell(ctx context.Context, target ShellTarget, cols, rows int) (ShellStatus, error) {
	if target.Namespace == "" || target.Pod == "" {
		return ShellStatus{}, userErrorf("A shell needs a namespace and a pod")
	}
	k, err := s.clusterClient(target.ClusterID)
	if err != nil {
		return ShellStatus{}, err
	}
	pod, err := k.client.CoreV1().Pods(target.Namespace).Get(ctx, target.Pod, metav1.GetOptions{})
	if err != nil {
		return ShellStatus{}, err
	}
	if target.Container == "" && len(pod.Spec.Containers) > 0 {
		target.Container = pod.Spec.Containers[0].Name
	}
	cfg := rest.CopyConfig(k.config)
	cfg.Timeout = 0
	rt, upgrader, err := spdyTransport(cfg)
	if err != nil {
		return ShellStatus{}, err
	}
	u, _, err := rest.DefaultServerUrlFor(cfg)
	if err != nil {
		return ShellStatus{}, err
	}
	u.Path = path.Join(u.Path, "api/v1/namespaces", target.Namespace, "pods", target.Pod, "exec")
	executorFor := func(shell string) (remotecommand.Executor, error) {
		q := url.Values{"container": {target.Container}, "command": {shell}, "stdin": {"true"}, "stdout": {"true"}, "tty": {"true"}}
		execURL := *u
		execURL.RawQuery = q.Encode()
		return remotecommand.NewSPDYExecutorForTransports(rt, upgrader, http.MethodPost, &execURL)
	}
	runCtx, cancel := context.WithCancel(context.Background())
	status := ShellStatus{ID: newID(), Target: target, State: StateIdle}
	sc := &shellConn{
		cancel: cancel,
		done:   make(chan struct{}),
		status: status,
		size:   remotecommand.TerminalSize{Width: uint16(cols), Height: uint16(rows)},
	}
	s.mu.Lock()
	s.shells[status.ID] = sc
	s.mu.Unlock()
	go s.runShell(runCtx, sc, executorFor)
	return status, nil
}

// WriteShell sends bytes to the session's stdin.
func (s *Service) WriteShell(sessionID string, data []byte) error {
	sc, err := s.shell(sessionID)
	if err != nil {
		return err
	}
	sc.mu.Lock()
	attempt := sc.attempt
	sc.mu.Unlock()
	if attempt == nil {
		return nil
	}
	_, err = attempt.stdin.Write(data)
	return err
}

// ResizeShell reports the terminal's new size to the remote session.
func (s *Service) ResizeShell(sessionID string, cols, rows int) error {
	sc, err := s.shell(sessionID)
	if err != nil {
		return err
	}
	sc.mu.Lock()
	defer sc.mu.Unlock()
	sc.size = remotecommand.TerminalSize{Width: uint16(cols), Height: uint16(rows)}
	if sc.attempt != nil {
		// ponytail: a full queue drops the resize; the next one carries the current size anyway.
		select {
		case sc.attempt.sizes <- sc.size:
		default:
		}
	}
	return nil
}

// StopShell ends the session, returning once it is stopped and forgotten; a session that already ended is forgotten too.
func (s *Service) StopShell(sessionID string) error {
	s.mu.Lock()
	sc := s.shells[sessionID]
	delete(s.shells, sessionID)
	s.mu.Unlock()
	if sc == nil {
		return nil
	}
	sc.cancel()
	<-sc.done
	return nil
}

// ShellStatuses returns every live shell session.
func (s *Service) ShellStatuses() []ShellStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]ShellStatus, 0, len(s.shells))
	for _, sc := range s.shells {
		sc.mu.Lock()
		out = append(out, sc.status)
		sc.mu.Unlock()
	}
	slices.SortFunc(out, func(a, b ShellStatus) int { return cmp.Compare(a.ID, b.ID) })
	return out
}

func (s *Service) shell(sessionID string) (*shellConn, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sc := s.shells[sessionID]
	if sc == nil {
		return nil, userErrorf("The shell session has ended")
	}
	return sc, nil
}

// runShell tries each shell in turn; one that fails before producing any output was not found, so the next is tried.
// The session is forgotten when the shell exits or none starts.
func (s *Service) runShell(ctx context.Context, sc *shellConn, executorFor func(shell string) (remotecommand.Executor, error)) {
	defer close(sc.done)
	var err error
	var started bool
	for _, shell := range shells {
		s.updateShellStatus(sc, func(st *ShellStatus) { st.Shell, st.State, st.Error = shell, StateConnecting, "" })
		started, err = s.execShell(ctx, sc, executorFor, shell)
		if ctx.Err() != nil {
			s.setShellState(sc, StateStopped, nil)
			return
		}
		if started || err == nil {
			// A shell that ran and exited non-zero ended normally as far as the session is concerned.
			var exit exec.CodeExitError
			if started && errors.As(err, &exit) {
				err = nil
			}
			break
		}
	}
	if !started && err != nil {
		err = &userError{msg: "The pod has none of " + strings.Join(shells, ", "), err: err}
	}
	s.mu.Lock()
	delete(s.shells, sc.status.ID)
	s.mu.Unlock()
	if err != nil {
		s.setShellState(sc, StateError, err)
		return
	}
	s.setShellState(sc, StateStopped, nil)
}

// execShell runs one shell to its end and reports whether it wrote anything, which is what tells a started shell
// from one the runtime could not find.
// ponytail: a runtime that echoes the not-found error onto the TTY would count as started; match its message if one shows up.
func (s *Service) execShell(ctx context.Context, sc *shellConn, executorFor func(shell string) (remotecommand.Executor, error), shell string) (bool, error) {
	exec, err := executorFor(shell)
	if err != nil {
		return false, err
	}
	r, w := io.Pipe()
	attempt := &shellAttempt{stdin: w, sizes: make(chan remotecommand.TerminalSize, 16)}
	sc.mu.Lock()
	attempt.sizes <- sc.size
	sc.attempt = attempt
	sc.mu.Unlock()
	defer func() {
		sc.mu.Lock()
		sc.attempt = nil
		sc.mu.Unlock()
		_ = w.Close()
		close(attempt.sizes)
	}()
	out := &shellOutput{svc: s, sc: sc}
	err = exec.StreamWithContext(ctx, remotecommand.StreamOptions{Stdin: r, Stdout: out, Tty: true, TerminalSizeQueue: sizeQueue(attempt.sizes)})
	return out.n > 0, err
}

// shellOutput emits each chunk the terminal produces; the first one marks the session connected.
type shellOutput struct {
	svc *Service
	sc  *shellConn
	n   int
}

func (o *shellOutput) Write(p []byte) (int, error) {
	if o.n == 0 {
		o.svc.setShellState(o.sc, StateConnected, nil)
	}
	o.n += len(p)
	o.svc.Emit(EventShellOutput, ShellOutput{SessionID: o.sc.status.ID, Data: slices.Clone(p)})
	return len(p), nil
}

type sizeQueue chan remotecommand.TerminalSize

func (q sizeQueue) Next() *remotecommand.TerminalSize {
	size, ok := <-q
	if !ok {
		return nil
	}
	return &size
}

func (s *Service) setShellState(sc *shellConn, state State, err error) {
	msg := errorMessage(err)
	s.updateShellStatus(sc, func(st *ShellStatus) { st.State, st.Error = state, msg })
}

func (s *Service) updateShellStatus(sc *shellConn, change func(st *ShellStatus)) {
	sc.mu.Lock()
	before := sc.status
	change(&sc.status)
	status := sc.status
	sc.mu.Unlock()
	if status != before {
		s.Emit(EventShellState, status)
	}
}

// spdyTransport builds the SPDY upgrader over the config's own transport, so the Route's dial function is honoured
// where client-go's spdy.RoundTripperFor would dial the API server directly.
func spdyTransport(cfg *rest.Config) (http.RoundTripper, spdy.Upgrader, error) {
	base, err := rest.TransportFor(cfg)
	if err != nil {
		return nil, nil, err
	}
	upgrader, err := streamspdy.NewRoundTripperWithConfig(streamspdy.RoundTripperConfig{UpgradeTransport: base, PingPeriod: 5 * time.Second})
	if err != nil {
		return nil, nil, err
	}
	rt, err := rest.HTTPWrappersForConfig(cfg, upgrader)
	if err != nil {
		return nil, nil, err
	}
	return rt, spdy.NewUpgraderForStreaming(upgrader), nil
}
