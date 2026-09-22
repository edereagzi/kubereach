package service

import (
	"bufio"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
)

type LogSourceKind string

const (
	LogSourcePod         LogSourceKind = "pod"
	LogSourceDeployment  LogSourceKind = "deployment"
	LogSourceStatefulSet LogSourceKind = "statefulset"
	LogSourceDaemonSet   LogSourceKind = "daemonset"
)

// LogSource is one pod, or a workload whose pods are followed together as they come and go.
type LogSource struct {
	ClusterID string        `json:"clusterId"`
	Namespace string        `json:"namespace"`
	Kind      LogSourceKind `json:"kind"`
	Name      string        `json:"name"`
	// Container narrows a pod source to one of its containers, init containers included.
	Container string `json:"container,omitempty"`
	// Previous reads the last terminated run once instead of following the current one.
	Previous bool `json:"previous,omitempty"`
}

// LogLine carries the raw text; a line that is a JSON object also carries its top-level fields as strings.
type LogLine struct {
	Pod       string            `json:"pod"`
	Container string            `json:"container"`
	Time      time.Time         `json:"time"`
	Text      string            `json:"text"`
	Fields    map[string]string `json:"fields,omitempty"`
}

// EventLogLines carries a LogBatch; lines are batched so a chatty pod does not flood the UI.
const EventLogLines = "logs:lines"

type LogBatch struct {
	StreamID string    `json:"streamId"`
	Lines    []LogLine `json:"lines"`
}

// EventLogState carries a LogStatus whenever its state, pods or containers change.
const EventLogState = "logs:state"

type LogStatus struct {
	ID     string    `json:"id"`
	Source LogSource `json:"source"`
	// Pods are the pods currently followed; Containers is the union of their containers.
	Pods       []string `json:"pods"`
	Containers []string `json:"containers"`
	State      State    `json:"state"`
	Error      string   `json:"error,omitempty"`
}

func (st LogStatus) equal(o LogStatus) bool {
	return st.State == o.State && st.Error == o.Error && slices.Equal(st.Pods, o.Pods) && slices.Equal(st.Containers, o.Containers)
}

const (
	logTailLines   = 1000
	logBatchMax    = 1000
	logBatchLinger = 50 * time.Millisecond
)

type logConn struct {
	cancel context.CancelFunc
	done   chan struct{}
	lines  chan LogLine
	mu     sync.Mutex
	status LogStatus
	// pods holds the cancel of each followed pod; open counts the log bodies currently streaming.
	pods map[string]context.CancelFunc
	open int
	wg   sync.WaitGroup
}

// StartLogs follows the source, delivering EventLogLines batches until StopLogs; a single pod's stream also ends when the pod is gone.
func (s *Service) StartLogs(ctx context.Context, src LogSource) (LogStatus, error) {
	if src.Namespace == "" || src.Name == "" {
		return LogStatus{}, errors.New("namespace and name are required")
	}
	if (src.Previous || src.Container != "") && src.Kind != LogSourcePod {
		return LogStatus{}, errors.New("previous logs and a single container are read from a single pod")
	}
	k, err := s.clusterClient(src.ClusterID)
	if err != nil {
		return LogStatus{}, err
	}
	var selector *metav1.LabelSelector
	var containers []corev1.Container
	switch src.Kind {
	case LogSourcePod:
		pod, err := k.client.CoreV1().Pods(src.Namespace).Get(ctx, src.Name, metav1.GetOptions{})
		if err != nil {
			return LogStatus{}, err
		}
		containers = pod.Spec.Containers
		if src.Container != "" {
			all := slices.Concat(pod.Spec.InitContainers, pod.Spec.Containers)
			i := slices.IndexFunc(all, func(c corev1.Container) bool { return c.Name == src.Container })
			if i < 0 {
				return LogStatus{}, fmt.Errorf("pod %s has no container %q", src.Name, src.Container)
			}
			containers = all[i : i+1]
		}
	case LogSourceDeployment:
		d, err := k.client.AppsV1().Deployments(src.Namespace).Get(ctx, src.Name, metav1.GetOptions{})
		if err != nil {
			return LogStatus{}, err
		}
		selector, containers = d.Spec.Selector, d.Spec.Template.Spec.Containers
	case LogSourceStatefulSet:
		ss, err := k.client.AppsV1().StatefulSets(src.Namespace).Get(ctx, src.Name, metav1.GetOptions{})
		if err != nil {
			return LogStatus{}, err
		}
		selector, containers = ss.Spec.Selector, ss.Spec.Template.Spec.Containers
	case LogSourceDaemonSet:
		ds, err := k.client.AppsV1().DaemonSets(src.Namespace).Get(ctx, src.Name, metav1.GetOptions{})
		if err != nil {
			return LogStatus{}, err
		}
		selector, containers = ds.Spec.Selector, ds.Spec.Template.Spec.Containers
	default:
		return LogStatus{}, fmt.Errorf("unknown log source kind %q", src.Kind)
	}
	var podSelector labels.Selector
	if selector != nil {
		if podSelector, err = metav1.LabelSelectorAsSelector(selector); err != nil {
			return LogStatus{}, err
		}
	}
	// Streams share the Cluster's transport, without the request timeout that would cut a follow short.
	cfg := rest.CopyConfig(k.config)
	cfg.Timeout = 0
	core, err := corev1client.NewForConfigAndClient(cfg, &http.Client{Transport: k.http.Transport})
	if err != nil {
		return LogStatus{}, err
	}
	runCtx, cancel := context.WithCancel(context.Background())
	status := LogStatus{ID: newID(), Source: src, Pods: []string{}, Containers: containerNames(containers), State: StateIdle}
	if selector == nil {
		status.Pods = []string{src.Name}
	}
	lc := &logConn{cancel: cancel, done: make(chan struct{}), lines: make(chan LogLine, logBatchMax), status: status, pods: map[string]context.CancelFunc{}}
	s.mu.Lock()
	s.logs[status.ID] = lc
	s.mu.Unlock()
	go s.runLogs(runCtx, lc, k, core.Pods(src.Namespace), podSelector)
	return status, nil
}

// StopLogs ends the stream, returning once it is stopped and forgotten; a stream that already ended is forgotten too.
func (s *Service) StopLogs(streamID string) error {
	s.mu.Lock()
	lc := s.logs[streamID]
	delete(s.logs, streamID)
	s.mu.Unlock()
	if lc == nil {
		return nil
	}
	lc.cancel()
	<-lc.done
	s.setLogState(lc, StateStopped, nil)
	return nil
}

// LogStatuses returns every log stream of this session.
func (s *Service) LogStatuses() []LogStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]LogStatus, 0, len(s.logs))
	for _, lc := range s.logs {
		lc.mu.Lock()
		out = append(out, lc.status)
		lc.mu.Unlock()
	}
	slices.SortFunc(out, func(a, b LogStatus) int { return cmp.Compare(a.ID, b.ID) })
	return out
}

func containerNames(containers []corev1.Container) []string {
	names := make([]string, 0, len(containers))
	for _, c := range containers {
		names = append(names, c.Name)
	}
	return names
}

// runLogs batches the merged lines of every followed pod. A single pod's stream that ended on its own stays
// listed in StateError so its lines remain readable until StopLogs.
func (s *Service) runLogs(ctx context.Context, lc *logConn, k kube, pods corev1client.PodInterface, selector labels.Selector) {
	defer close(lc.done)
	s.setLogState(lc, StateConnecting, nil)
	readersDone := make(chan struct{})
	var gone error
	go func() {
		defer close(readersDone)
		if selector == nil {
			gone = s.followPod(ctx, lc, pods, lc.status.Source.Name, lc.status.Containers)
		} else {
			s.watchWorkload(ctx, lc, k, pods, selector)
		}
		lc.wg.Wait()
	}()
	batchInto(ctx, lc.lines, readersDone, func(batch []LogLine) { s.emitLogs(lc, batch) })
	<-readersDone
	if ctx.Err() != nil {
		s.setLogState(lc, StateStopped, nil)
		return
	}
	// Readers only finish on their own when the pod is gone or a previous run has been read in full.
	if gone == nil {
		s.setLogState(lc, StateIdle, nil)
		return
	}
	s.setLogState(lc, StateError, gone)
}

// watchWorkload keeps one follower per pod matching the selector, joining and leaving as the informer reports them.
func (s *Service) watchWorkload(ctx context.Context, lc *logConn, k kube, pods corev1client.PodInterface, selector labels.Selector) {
	list := k.client.CoreV1().Pods(lc.status.Source.Namespace)
	_, ctrl := cache.NewInformerWithOptions(cache.InformerOptions{
		ListerWatcher: plainListWatch{&cache.ListWatch{
			ListWithContextFunc: func(ctx context.Context, o metav1.ListOptions) (runtime.Object, error) {
				o.LabelSelector = selector.String()
				return list.List(ctx, o)
			},
			WatchFuncWithContext: func(ctx context.Context, o metav1.ListOptions) (watch.Interface, error) {
				o.LabelSelector = selector.String()
				return list.Watch(ctx, o)
			},
		}},
		ObjectType: &corev1.Pod{},
		Handler: cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj any) {
				if pod := obj.(*corev1.Pod); selector.Matches(labels.Set(pod.Labels)) {
					s.followWorkloadPod(ctx, lc, pods, pod)
				}
			},
			DeleteFunc: func(obj any) {
				if d, ok := obj.(cache.DeletedFinalStateUnknown); ok {
					obj = d.Obj
				}
				if pod, ok := obj.(*corev1.Pod); ok {
					s.leaveWorkloadPod(lc, pod.Name)
				}
			},
		},
	})
	ctrl.RunWithContext(ctx)
}

// plainListWatch opts out of watch-list semantics, which the fake clientset in tests does not speak; plain list+watch works everywhere.
type plainListWatch struct{ *cache.ListWatch }

func (plainListWatch) IsWatchListSemanticsUnSupported() bool { return true }

func (s *Service) followWorkloadPod(ctx context.Context, lc *logConn, pods corev1client.PodInterface, pod *corev1.Pod) {
	containers := containerNames(pod.Spec.Containers)
	podCtx, cancel := context.WithCancel(ctx)
	lc.mu.Lock()
	if lc.pods[pod.Name] != nil {
		lc.mu.Unlock()
		cancel()
		return
	}
	lc.pods[pod.Name] = cancel
	lc.wg.Go(func() {
		defer cancel()
		if s.followPod(podCtx, lc, pods, pod.Name, containers) != nil {
			s.leaveWorkloadPod(lc, pod.Name)
		}
	})
	lc.mu.Unlock()
	s.updateLogStatus(lc, func(st *LogStatus) {
		st.Pods = slices.Sorted(maps.Keys(lc.pods))
		for _, c := range containers {
			if !slices.Contains(st.Containers, c) {
				st.Containers = append(st.Containers, c)
			}
		}
	})
}

func (s *Service) leaveWorkloadPod(lc *logConn, name string) {
	lc.mu.Lock()
	cancel := lc.pods[name]
	delete(lc.pods, name)
	lc.mu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	// A pod's bodies end before its deletion arrives; with none left to stream the watch itself is what is connected.
	s.updateLogStatus(lc, func(st *LogStatus) {
		st.Pods = slices.Sorted(maps.Keys(lc.pods))
		if lc.open == 0 {
			st.State, st.Error = StateConnected, ""
		}
	})
}

// followPod follows every container of one pod until ctx is cancelled or the pod is gone, which it reports.
func (s *Service) followPod(ctx context.Context, lc *logConn, pods corev1client.PodInterface, pod string, containers []string) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	var mu sync.Mutex
	var gone error
	for _, container := range containers {
		wg.Go(func() {
			if err := s.followContainer(ctx, lc, pods, pod, container); err != nil {
				mu.Lock()
				gone = cmp.Or(gone, err)
				mu.Unlock()
				cancel()
			}
		})
	}
	wg.Wait()
	return gone
}

// followContainer re-follows the container from the last line seen after every drop, until ctx is cancelled;
// it returns an error only when the pod no longer exists. Reconnecting is reported only while no body at all is open.
// ponytail: any other request failure retries forever and is invisible while another container streams; per-container state if it matters.
func (s *Service) followContainer(ctx context.Context, lc *logConn, pods corev1client.PodInterface, pod, container string) error {
	var since time.Time
	backoff := time.Second
	for {
		opened, err := s.readContainerLogs(ctx, lc, pods, pod, container, &since)
		if ctx.Err() != nil {
			return nil
		}
		if apierrors.IsNotFound(err) {
			return err
		}
		// A previous run is read once; a container without one is answered with 400, which is the same nothing to read.
		if lc.status.Source.Previous && (opened || apierrors.IsBadRequest(err)) {
			return nil
		}
		if opened {
			backoff = time.Second
		}
		if lc.openBodies(0) == 0 {
			s.setLogState(lc, StateReconnecting, cmp.Or(err, errLogsEnded))
		}
		// A Route that is down is polled every second so the stream resumes as soon as it is back.
		wait := backoff
		if errors.Is(err, ErrRouteDown) {
			wait = time.Second
		} else {
			backoff = min(backoff*2, 30*time.Second)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(wait):
		}
	}
}

var errLogsEnded = errors.New("log stream ended")

func (lc *logConn) openBodies(delta int) int {
	lc.mu.Lock()
	defer lc.mu.Unlock()
	lc.open += delta
	return lc.open
}

// readContainerLogs streams one follow body, reporting whether it opened. The first read tails the backlog;
// later reads start at the last line seen. SinceTime has second precision, so the overlap is dropped here.
func (s *Service) readContainerLogs(ctx context.Context, lc *logConn, pods corev1client.PodInterface, pod, container string, since *time.Time) (bool, error) {
	previous := lc.status.Source.Previous
	opts := &corev1.PodLogOptions{Container: container, Follow: !previous, Previous: previous, Timestamps: true}
	cutoff := *since
	if cutoff.IsZero() {
		tail := int64(logTailLines)
		opts.TailLines = &tail
	} else {
		t := metav1.NewTime(cutoff)
		opts.SinceTime = &t
	}
	body, err := pods.GetLogs(pod, opts).Stream(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = body.Close() }()
	lc.openBodies(1)
	defer lc.openBodies(-1)
	s.setLogState(lc, StateConnected, nil)
	sc := bufio.NewScanner(body)
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		l := parseLogLine(pod, container, sc.Text())
		if !cutoff.IsZero() && !l.Time.IsZero() && !l.Time.After(cutoff) {
			continue
		}
		if l.Time.After(*since) {
			*since = l.Time
		}
		select {
		case lc.lines <- l:
		case <-ctx.Done():
			return true, nil
		}
	}
	return true, sc.Err()
}

// parseLogLine splits the RFC3339Nano prefix the API server adds with timestamps=true; a line without one keeps its full text.
func parseLogLine(pod, container, raw string) LogLine {
	l := LogLine{Pod: pod, Container: container, Text: raw}
	if stamp, text, ok := strings.Cut(raw, " "); ok {
		if t, err := time.Parse(time.RFC3339Nano, stamp); err == nil {
			l.Time, l.Text = t, text
		}
	}
	l.Fields = parseJSONFields(l.Text)
	return l
}

// parseJSONFields flattens a JSON object's top-level values to strings: strings as they are, anything else as its JSON.
func parseJSONFields(text string) map[string]string {
	if !strings.HasPrefix(text, "{") {
		return nil
	}
	var raw map[string]json.RawMessage
	if json.Unmarshal([]byte(text), &raw) != nil {
		return nil
	}
	fields := make(map[string]string, len(raw))
	for k, v := range raw {
		var str string
		if json.Unmarshal(v, &str) == nil {
			fields[k] = str
		} else {
			fields[k] = string(v)
		}
	}
	return fields
}

// batchInto emits items as they arrive, waiting up to logBatchLinger to fill a batch, until the producers are done.
func batchInto[T any](ctx context.Context, items <-chan T, producersDone <-chan struct{}, emit func([]T)) {
	for {
		var batch []T
		select {
		case <-ctx.Done():
			return
		case <-producersDone:
			for len(items) > 0 {
				batch = append(batch, <-items)
			}
			if len(batch) > 0 {
				emit(batch)
			}
			return
		case l := <-items:
			batch = append(batch, l)
		}
		linger := time.After(logBatchLinger)
	fill:
		for len(batch) < logBatchMax {
			select {
			case l := <-items:
				batch = append(batch, l)
			case <-linger:
				break fill
			case <-ctx.Done():
				return
			}
		}
		emit(batch)
	}
}

// emitLogs orders a batch by timestamp so the containers' backlogs interleave chronologically.
func (s *Service) emitLogs(lc *logConn, batch []LogLine) {
	slices.SortStableFunc(batch, func(a, b LogLine) int { return a.Time.Compare(b.Time) })
	s.Emit(EventLogLines, LogBatch{StreamID: lc.status.ID, Lines: batch})
}

func (s *Service) setLogState(lc *logConn, state State, err error) {
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	s.updateLogStatus(lc, func(st *LogStatus) { st.State, st.Error = state, msg })
}

// updateLogStatus applies change under the lock and emits the status if anything changed.
func (s *Service) updateLogStatus(lc *logConn, change func(st *LogStatus)) {
	lc.mu.Lock()
	before := lc.status
	change(&lc.status)
	status := lc.status
	lc.mu.Unlock()
	if !status.equal(before) {
		s.Emit(EventLogState, status)
	}
}
