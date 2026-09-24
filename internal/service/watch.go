package service

import (
	"context"
	"slices"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/tools/cache"
)

// EventClusterChanged carries a ClusterChange once the watched objects of a Cluster change, coalesced over watchCoalesce.
const EventClusterChanged = "cluster:changed"

// ClusterChange names the lists of a Cluster that changed or began or stopped failing; a kind is the list's query key, such as "pods" or "nodes".
type ClusterChange struct {
	ClusterID string   `json:"clusterId"`
	Kinds     []string `json:"kinds"`
}

const watchCoalesce = 500 * time.Millisecond

var errClientReplaced error = &userError{msg: "The Cluster's watch was restarted; try again"}

// watchCache holds a Cluster's informers, started per kind on first read and serving every later read without the Route.
type watchCache struct {
	ctx       context.Context
	cancel    context.CancelFunc
	clusterID string
	emit      func(name string, data any)
	mu        sync.Mutex
	kinds     map[string]*watchedKind
	pending   []string
}

type watchedKind struct {
	ctx    context.Context
	cancel context.CancelFunc
	stores []cache.Store
	ctrls  []cache.Controller
	mu     sync.Mutex
	// errs holds each namespace's last list or watch error, nil once it succeeds.
	errs map[string]error
}

type listWatchFuncs struct {
	list  func(ctx context.Context, namespace string, o metav1.ListOptions) (runtime.Object, error)
	watch func(ctx context.Context, namespace string, o metav1.ListOptions) (watch.Interface, error)
}

func newWatchCache(clusterID string, emit func(string, any)) *watchCache {
	ctx, cancel := context.WithCancel(context.Background())
	return &watchCache{ctx: ctx, cancel: cancel, clusterID: clusterID, emit: emit, kinds: map[string]*watchedKind{}}
}

func (w *watchCache) stop() {
	w.cancel()
	w.reset()
}

// reset stops every informer; the next read starts them again, so a Route that came back is not waited out on the reflector's backoff.
func (w *watchCache) reset() {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, k := range w.kinds {
		k.cancel()
	}
	clear(w.kinds)
}

// list returns the objects of the informers under key once they have synced. A failing list or watch is returned even after
// sync, so a Cluster that stopped answering is not shown as its last known state. kinds are announced when they change.
func (w *watchCache) list(ctx context.Context, key string, kinds []string, namespaces []string, obj runtime.Object, lw listWatchFuncs) ([]any, error) {
	w.mu.Lock()
	if w.ctx.Err() != nil {
		w.mu.Unlock()
		return nil, errClientReplaced
	}
	k := w.kinds[key]
	if k == nil {
		k = w.start(kinds, namespaces, obj, lw)
		w.kinds[key] = k
	}
	w.mu.Unlock()
	err := wait.PollUntilContextCancel(ctx, 50*time.Millisecond, true, func(context.Context) (bool, error) {
		if k.ctx.Err() != nil {
			return false, errClientReplaced
		}
		synced := !slices.ContainsFunc(k.ctrls, func(c cache.Controller) bool { return !c.HasSynced() })
		k.mu.Lock()
		defer k.mu.Unlock()
		for ns, err := range k.errs {
			if err != nil {
				return false, wrapForbidden(&namespaceError{namespace: ns, err: err})
			}
		}
		return synced, nil
	})
	if err != nil {
		return nil, err
	}
	var out []any
	for _, s := range k.stores {
		out = append(out, s.List()...)
	}
	return out, nil
}

func (w *watchCache) start(kinds []string, namespaces []string, obj runtime.Object, lw listWatchFuncs) *watchedKind {
	ctx, cancel := context.WithCancel(w.ctx)
	k := &watchedKind{ctx: ctx, cancel: cancel, errs: map[string]error{}}
	changed := func() { w.changed(ctx, kinds) }
	for _, ns := range namespaces {
		report := func(err error) {
			k.mu.Lock()
			failed := k.errs[ns] != nil
			k.errs[ns] = err
			k.mu.Unlock()
			if failed != (err != nil) {
				changed()
			}
		}
		store, ctrl := cache.NewInformerWithOptions(cache.InformerOptions{
			ListerWatcher: plainListWatch{&cache.ListWatch{
				ListWithContextFunc: func(ctx context.Context, o metav1.ListOptions) (runtime.Object, error) {
					list, err := lw.list(ctx, ns, o)
					report(err)
					return list, err
				},
				WatchFuncWithContext: func(ctx context.Context, o metav1.ListOptions) (watch.Interface, error) {
					wi, err := lw.watch(ctx, ns, o)
					report(err)
					return wi, err
				},
			}},
			ObjectType: obj,
			Handler: cache.ResourceEventHandlerDetailedFuncs{
				AddFunc: func(_ any, initial bool) {
					if !initial {
						changed()
					}
				},
				UpdateFunc: func(any, any) { changed() },
				DeleteFunc: func(any) { changed() },
			},
		})
		k.stores, k.ctrls = append(k.stores, store), append(k.ctrls, ctrl)
		go ctrl.RunWithContext(ctx)
	}
	return k
}

// changed queues kinds for the next coalesced ClusterChange; ctx is the informer's, so a stopped one announces nothing.
func (w *watchCache) changed(ctx context.Context, kinds []string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if ctx.Err() != nil {
		return
	}
	if len(w.pending) == 0 {
		time.AfterFunc(watchCoalesce, func() {
			w.mu.Lock()
			kinds := w.pending
			w.pending = nil
			w.mu.Unlock()
			if w.ctx.Err() == nil {
				slices.Sort(kinds)
				w.emit(EventClusterChanged, ClusterChange{ClusterID: w.clusterID, Kinds: kinds})
			}
		})
	}
	for _, kind := range kinds {
		if !slices.Contains(w.pending, kind) {
			w.pending = append(w.pending, kind)
		}
	}
}

// scopedPods are the pods of the Cluster's namespace scope. When that is every namespace they are also what the nodes'
// totals are summed from, so a change announces the nodes too.
func scopedPods(ctx context.Context, k kube) ([]*corev1.Pod, error) {
	kinds := []string{"pods"}
	if len(k.cluster.Namespaces) == 0 {
		kinds = append(kinds, "nodes")
	}
	return cachedPods(ctx, k, "pods", kinds, k.scope())
}

// nodePods are every namespace's pods for the nodes' totals of a Cluster scoped to some namespaces.
func nodePods(ctx context.Context, k kube) ([]*corev1.Pod, error) {
	return cachedPods(ctx, k, "node-pods", []string{"nodes"}, []string{metav1.NamespaceAll})
}

func cachedPods(ctx context.Context, k kube, key string, kinds, namespaces []string) ([]*corev1.Pod, error) {
	return cached(ctx, k, key, kinds, namespaces, &corev1.Pod{}, func(ns string) listWatcher[*corev1.PodList] { return k.client.CoreV1().Pods(ns) })
}

func cachedNodes(ctx context.Context, k kube) ([]*corev1.Node, error) {
	return cached(ctx, k, "nodes", []string{"nodes"}, []string{metav1.NamespaceAll}, &corev1.Node{}, func(string) listWatcher[*corev1.NodeList] { return k.client.CoreV1().Nodes() })
}

type listWatcher[L runtime.Object] interface {
	List(context.Context, metav1.ListOptions) (L, error)
	Watch(context.Context, metav1.ListOptions) (watch.Interface, error)
}

func cached[T, L runtime.Object](ctx context.Context, k kube, key string, kinds, namespaces []string, obj T, client func(ns string) listWatcher[L]) ([]T, error) {
	objs, err := k.watch.list(ctx, key, kinds, namespaces, obj, listWatchFuncs{
		list: func(ctx context.Context, ns string, o metav1.ListOptions) (runtime.Object, error) {
			return client(ns).List(ctx, o)
		},
		watch: func(ctx context.Context, ns string, o metav1.ListOptions) (watch.Interface, error) {
			return client(ns).Watch(ctx, o)
		},
	})
	return typed[T](objs), err
}

func typed[T any](objs []any) []T {
	out := make([]T, 0, len(objs))
	for _, o := range objs {
		out = append(out, o.(T))
	}
	return out
}
