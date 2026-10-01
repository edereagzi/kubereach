package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"reflect"
	"unicode/utf8"

	"github.com/pmezard/go-difflib/difflib"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/yaml"
)

// ObjectKind names an object GetYAML can fetch; the lowercase Kubernetes kind.
type ObjectKind string

const (
	ObjectPod         ObjectKind = "pod"
	ObjectDeployment  ObjectKind = "deployment"
	ObjectStatefulSet ObjectKind = "statefulset"
	ObjectDaemonSet   ObjectKind = "daemonset"
	ObjectCronJob     ObjectKind = "cronjob"
	ObjectJob         ObjectKind = "job"
	ObjectService     ObjectKind = "service"
	ObjectConfigMap   ObjectKind = "configmap"
	ObjectSecret      ObjectKind = "secret"
	ObjectIngress     ObjectKind = "ingress"
	ObjectPVC         ObjectKind = "persistentvolumeclaim"
	ObjectHPA         ObjectKind = "horizontalpodautoscaler"
	ObjectNode        ObjectKind = "node"
)

const (
	maskedValue = "********"
	// lastApplied carries the whole object as kubectl applied it, Secret values included.
	lastApplied = "kubectl.kubernetes.io/last-applied-configuration"
)

// GetYAML is the object as kubectl get -o yaml shows it, without managedFields; a Secret's values are masked unless reveal is set.
func (s *Service) GetYAML(ctx context.Context, clusterID string, kind ObjectKind, namespace, name string, reveal bool) (string, error) {
	c, err := s.objectClient(clusterID, kind, namespace)
	if err != nil {
		return "", err
	}
	obj, err := c.get(ctx, name)
	if err != nil {
		return "", wrapForbidden(err)
	}
	m, err := objectMap(obj)
	if err != nil {
		return "", err
	}
	if kind == ObjectSecret && !reveal {
		if data, ok := m["data"].(map[string]any); ok {
			for key := range data {
				data[key] = maskedValue
			}
		}
		md, _ := m["metadata"].(map[string]any)
		if ann, ok := md["annotations"].(map[string]any); ok && ann[lastApplied] != nil {
			ann[lastApplied] = maskedValue
		}
	}
	out, err := yaml.Marshal(m)
	return string(out), err
}

// EditYAML is the object as the editor opens it (see editMap): without status, which an update never changes, and a
// Secret's values decoded, never masked. Its resourceVersion goes back with the edit, so a change made in the meantime
// is a conflict instead of being overwritten.
func (s *Service) EditYAML(ctx context.Context, clusterID string, kind ObjectKind, namespace, name string) (string, error) {
	c, err := s.objectClient(clusterID, kind, namespace)
	if err != nil {
		return "", err
	}
	obj, err := c.get(ctx, name)
	if err != nil {
		return "", wrapForbidden(err)
	}
	return editText(obj)
}

// DiffYAML is what applying text would change, as a unified diff from the live object to the server's dry run of the
// update. original is the text EditYAML gave the editor.
func (s *Service) DiffYAML(ctx context.Context, clusterID string, kind ObjectKind, namespace, name, original, text string) (string, error) {
	c, live, obj, err := s.editedObject(ctx, clusterID, kind, namespace, name, original, text)
	if err != nil {
		return "", err
	}
	result, err := c.update(ctx, obj, metav1.UpdateOptions{DryRun: []string{metav1.DryRunAll}})
	if err != nil {
		return "", wrapForbidden(err)
	}
	before, err := editText(live)
	if err != nil {
		return "", err
	}
	after, err := editText(result)
	if err != nil {
		return "", err
	}
	return difflib.GetUnifiedDiffString(difflib.UnifiedDiff{A: difflib.SplitLines(before), B: difflib.SplitLines(after), Context: 3})
}

// ApplyYAML updates the object to text; status is never sent. original is the text EditYAML gave the editor.
func (s *Service) ApplyYAML(ctx context.Context, clusterID string, kind ObjectKind, namespace, name, original, text string) error {
	c, _, obj, err := s.editedObject(ctx, clusterID, kind, namespace, name, original, text)
	if err != nil {
		return err
	}
	_, err = c.update(ctx, obj, metav1.UpdateOptions{})
	return wrapForbidden(err)
}

// strictCodec refuses a field the kind does not have, which a lenient decode would drop without a word.
var strictCodec = serializer.NewCodecFactory(scheme.Scheme, serializer.EnableStrict).UniversalDeserializer()

// editedObject decodes the editor's text into the object it must stay: the same kind, name and namespace. It keeps the
// resourceVersion the editor loaded, unless the live object changed only in status since; controllers write that all
// the time, and the update never sends it, so the edit overwrites nothing and goes on over the live version.
func (s *Service) editedObject(ctx context.Context, clusterID string, kind ObjectKind, namespace, name, original, text string) (c objectClient, live, obj runtime.Object, err error) {
	if c, err = s.objectClient(clusterID, kind, namespace); err != nil {
		return
	}
	if obj, _, err = strictCodec.Decode([]byte(text), nil, nil); err != nil {
		// The decoder's words point at the user's own text, which is what they need to fix it.
		err = &userError{msg: "The YAML is not valid: " + err.Error()}
		return
	}
	if !c.is(obj) {
		err = userErrorf("apiVersion and kind cannot change")
		return
	}
	m, err := meta.Accessor(obj)
	if err != nil {
		return
	}
	if m.GetName() != name || m.GetNamespace() != namespace {
		err = userErrorf("The name and namespace cannot change")
		return
	}
	// Kinds with a status keep it in a Status field (a ConfigMap or Secret has none); the typed clients have no other
	// way to leave it out.
	if status := reflect.ValueOf(obj).Elem().FieldByName("Status"); status.IsValid() {
		status.SetZero()
	}
	// A Secret's text values come back under stringData; they are encoded into data here, as the API server would,
	// stringData winning over data for the same key.
	if sec, ok := obj.(*corev1.Secret); ok {
		if sec.Data == nil && len(sec.StringData) > 0 {
			sec.Data = map[string][]byte{}
		}
		for k, v := range sec.StringData {
			sec.Data[k] = []byte(v)
		}
		sec.StringData = nil
	}
	if live, err = c.get(ctx, name); err != nil {
		err = wrapForbidden(err)
		return
	}
	if unchangedSince(live, original) {
		lm, _ := meta.Accessor(live)
		m.SetResourceVersion(lm.GetResourceVersion())
	}
	return
}

// unchangedSince reports whether live is still the object original showed, status and resourceVersion aside.
func unchangedSince(live runtime.Object, original string) bool {
	now, err := editMap(live)
	if err != nil {
		return false
	}
	var then map[string]any
	if yaml.Unmarshal([]byte(original), &then) != nil {
		return false
	}
	for _, m := range []map[string]any{now, then} {
		if md, ok := m["metadata"].(map[string]any); ok {
			delete(md, "resourceVersion")
		}
	}
	return reflect.DeepEqual(now, then)
}

// objectClient reaches one kind's objects through the typed clientset.
type objectClient struct {
	get    func(context.Context, string) (runtime.Object, error)
	update func(context.Context, runtime.Object, metav1.UpdateOptions) (runtime.Object, error)
	delete func(context.Context, string, metav1.DeleteOptions) error
	is     func(runtime.Object) bool
}

type typedClient[T runtime.Object] interface {
	Get(context.Context, string, metav1.GetOptions) (T, error)
	Update(context.Context, T, metav1.UpdateOptions) (T, error)
	Delete(context.Context, string, metav1.DeleteOptions) error
}

func typedObjects[T runtime.Object](c typedClient[T]) objectClient {
	return objectClient{
		get: func(ctx context.Context, name string) (runtime.Object, error) {
			return c.Get(ctx, name, metav1.GetOptions{})
		},
		update: func(ctx context.Context, obj runtime.Object, opts metav1.UpdateOptions) (runtime.Object, error) {
			return c.Update(ctx, obj.(T), opts)
		},
		delete: c.Delete,
		is:     func(obj runtime.Object) bool { _, ok := obj.(T); return ok },
	}
}

func (s *Service) objectClient(clusterID string, kind ObjectKind, namespace string) (objectClient, error) {
	k, err := s.clusterClient(clusterID)
	if err != nil {
		return objectClient{}, err
	}
	switch kind {
	case ObjectPod:
		return typedObjects(k.client.CoreV1().Pods(namespace)), nil
	case ObjectDeployment:
		return typedObjects(k.client.AppsV1().Deployments(namespace)), nil
	case ObjectStatefulSet:
		return typedObjects(k.client.AppsV1().StatefulSets(namespace)), nil
	case ObjectDaemonSet:
		return typedObjects(k.client.AppsV1().DaemonSets(namespace)), nil
	case ObjectCronJob:
		return typedObjects(k.client.BatchV1().CronJobs(namespace)), nil
	case ObjectJob:
		return typedObjects(k.client.BatchV1().Jobs(namespace)), nil
	case ObjectService:
		return typedObjects(k.client.CoreV1().Services(namespace)), nil
	case ObjectConfigMap:
		return typedObjects(k.client.CoreV1().ConfigMaps(namespace)), nil
	case ObjectSecret:
		return typedObjects(k.client.CoreV1().Secrets(namespace)), nil
	case ObjectIngress:
		return typedObjects(k.client.NetworkingV1().Ingresses(namespace)), nil
	case ObjectPVC:
		return typedObjects(k.client.CoreV1().PersistentVolumeClaims(namespace)), nil
	case ObjectHPA:
		return typedObjects(k.client.AutoscalingV2().HorizontalPodAutoscalers(namespace)), nil
	case ObjectNode:
		return typedObjects(k.client.CoreV1().Nodes()), nil
	}
	return objectClient{}, fmt.Errorf("unknown object kind %q", kind)
}

// objectMap is obj as a map the same shape as kubectl's output, without managedFields. Typed clients drop apiVersion and
// kind, which the scheme gives back.
func objectMap(obj runtime.Object) (map[string]any, error) {
	gvks, _, err := scheme.Scheme.ObjectKinds(obj)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(obj)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	m["apiVersion"], m["kind"] = gvks[0].GroupVersion().String(), gvks[0].Kind
	md, _ := m["metadata"].(map[string]any)
	delete(md, "managedFields")
	return m, nil
}

func editText(obj runtime.Object) (string, error) {
	m, err := editMap(obj)
	if err != nil {
		return "", err
	}
	out, err := yaml.Marshal(m)
	if _, secret := obj.(*corev1.Secret); secret && m["data"] != nil {
		// The top-level data key is the only one at the start of a line.
		out = bytes.Replace(out, []byte("\ndata:\n"), []byte("\n# Values under data are not text and stay base64; text values are under stringData.\ndata:\n"), 1)
	}
	return string(out), err
}

// editMap is obj as the editor shows it: without status, and a Secret's text values decoded under stringData, while
// a value YAML cannot carry as text stays base64 under data.
func editMap(obj runtime.Object) (map[string]any, error) {
	m, err := objectMap(obj)
	if err != nil {
		return nil, err
	}
	delete(m, "status")
	if sec, ok := obj.(*corev1.Secret); ok {
		data, text := map[string]any{}, map[string]any{}
		for k, v := range sec.Data {
			if isText(v) {
				text[k] = string(v)
			} else {
				data[k] = base64.StdEncoding.EncodeToString(v)
			}
		}
		delete(m, "data")
		if len(data) > 0 {
			m["data"] = data
		}
		if len(text) > 0 {
			m["stringData"] = text
		}
	}
	return m, nil
}

// isText reports whether v is UTF-8 that YAML writes and reads back exactly; YAML refuses some control characters.
func isText(v []byte) bool {
	if !utf8.Valid(v) {
		return false
	}
	out, err := yaml.Marshal(string(v))
	var back string
	return err == nil && yaml.Unmarshal(out, &back) == nil && back == string(v)
}
