package service

import (
	"context"
	"slices"
	"sync"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/tools/cache"
)

// EventEventBatch carries an EventBatch, newest first; a repeated event is delivered again under the same ID.
const EventEventBatch = "events:batch"

type EventBatch struct {
	StreamID string      `json:"streamId"`
	Events   []KubeEvent `json:"events"`
}

// EventEventState carries an EventStatus on every transition.
const EventEventState = "events:state"

// EventStatus is one Cluster's event watch over its namespace scope.
type EventStatus struct {
	ID        string `json:"id"`
	ClusterID string `json:"clusterId"`
	State     State  `json:"state"`
	Error     string `json:"error,omitempty"`
}

type eventConn struct {
	cancel context.CancelFunc
	done   chan struct{}
	items  chan KubeEvent
	mu     sync.Mutex
	status EventStatus
	// errs holds each namespace's last list or watch error, nil once it is watching.
	errs map[string]error
}

// StartEvents watches the events of the Cluster's namespace scope, delivering EventEventBatch batches until StopEvents.
// A Cluster is watched once: an earlier stream of the same Cluster is stopped first. The watch resumes on its own after a Route reconnect.
func (s *Service) StartEvents(clusterID string) (EventStatus, error) {
	k, err := s.clusterClient(clusterID)
	if err != nil {
		return EventStatus{}, err
	}
	s.mu.Lock()
	var previous []string
	for id, ec := range s.events {
		if ec.status.ClusterID == clusterID {
			previous = append(previous, id)
		}
	}
	s.mu.Unlock()
	for _, id := range slices.Sorted(slices.Values(previous)) {
		_ = s.StopEvents(id)
	}
	runCtx, cancel := context.WithCancel(context.Background())
	status := EventStatus{ID: newID(), ClusterID: clusterID, State: StateIdle}
	ec := &eventConn{cancel: cancel, done: make(chan struct{}), items: make(chan KubeEvent, logBatchMax), status: status, errs: map[string]error{}}
	s.mu.Lock()
	s.events[status.ID] = ec
	s.mu.Unlock()
	go s.runEvents(runCtx, ec, k)
	return status, nil
}

// StopEvents ends the watch, returning once it is stopped and forgotten; an unknown stream is a no-op.
func (s *Service) StopEvents(streamID string) error {
	s.mu.Lock()
	ec := s.events[streamID]
	delete(s.events, streamID)
	s.mu.Unlock()
	if ec == nil {
		return nil
	}
	ec.cancel()
	<-ec.done
	return nil
}

func (s *Service) runEvents(ctx context.Context, ec *eventConn, k kube) {
	defer close(ec.done)
	s.setEventState(ec, StateConnecting, nil)
	watchersDone := make(chan struct{})
	go func() {
		defer close(watchersDone)
		var wg sync.WaitGroup
		for _, ns := range k.scope() {
			wg.Go(func() { s.watchEvents(ctx, ec, ns, k.client.CoreV1().Events(ns)) })
		}
		wg.Wait()
	}()
	batchInto(ctx, ec.items, watchersDone, func(batch []KubeEvent) {
		sortEventsNewestFirst(batch)
		s.Emit(EventEventBatch, EventBatch{StreamID: ec.status.ID, Events: batch})
	})
	<-watchersDone
	s.setEventState(ec, StateStopped, nil)
}

// watchEvents runs one informer over a namespace; the informer relists and rewatches on its own after every drop,
// so the state is read off the list and watch calls themselves.
// ponytail: the informer's client carries the 15s request timeout, so each watch body is cut and reopened at that pace,
// and a dropped Route is retried on the reflector's backoff (up to 30s) rather than every second; a timeout-free client if it shows.
func (s *Service) watchEvents(ctx context.Context, ec *eventConn, namespace string, events corev1client.EventInterface) {
	report := func(err error) {
		if ctx.Err() != nil {
			return
		}
		ec.mu.Lock()
		ec.errs[namespace] = err
		ec.mu.Unlock()
		s.reportEventState(ec)
	}
	deliver := func(obj any) {
		if e, ok := obj.(*corev1.Event); ok {
			select {
			case ec.items <- newKubeEvent(e):
			case <-ctx.Done():
			}
		}
	}
	_, ctrl := cache.NewInformerWithOptions(cache.InformerOptions{
		ListerWatcher: plainListWatch{&cache.ListWatch{
			ListWithContextFunc: func(ctx context.Context, o metav1.ListOptions) (runtime.Object, error) {
				list, err := events.List(ctx, o)
				report(err)
				return list, err
			},
			WatchFuncWithContext: func(ctx context.Context, o metav1.ListOptions) (watch.Interface, error) {
				w, err := events.Watch(ctx, o)
				report(err)
				return w, err
			},
		}},
		ObjectType: &corev1.Event{},
		Handler: cache.ResourceEventHandlerFuncs{
			AddFunc:    deliver,
			UpdateFunc: func(_, obj any) { deliver(obj) },
		},
	})
	ctrl.RunWithContext(ctx)
}

// reportEventState folds the namespaces' errors into one state: a forbidden namespace is an error that stays visible,
// any other failing namespace means reconnecting, and none failing means connected.
func (s *Service) reportEventState(ec *eventConn) {
	ec.mu.Lock()
	var forbidden, failing error
	for ns, err := range ec.errs {
		if apierrors.IsForbidden(err) {
			forbidden = wrapForbidden(&namespaceError{namespace: ns, err: err})
		} else if err != nil {
			failing = err
		}
	}
	ec.mu.Unlock()
	switch {
	case forbidden != nil:
		s.setEventState(ec, StateError, forbidden)
	case failing != nil:
		s.setEventState(ec, StateReconnecting, failing)
	default:
		s.setEventState(ec, StateConnected, nil)
	}
}

func (s *Service) setEventState(ec *eventConn, state State, err error) {
	msg := errorMessage(err)
	ec.mu.Lock()
	before := ec.status
	ec.status.State, ec.status.Error = state, msg
	status := ec.status
	ec.mu.Unlock()
	if status != before {
		s.Emit(EventEventState, status)
	}
}
