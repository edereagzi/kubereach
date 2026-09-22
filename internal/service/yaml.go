package service

import (
	"context"
	"encoding/json"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
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
	ObjectService     ObjectKind = "service"
	ObjectConfigMap   ObjectKind = "configmap"
	ObjectSecret      ObjectKind = "secret"
	ObjectIngress     ObjectKind = "ingress"
)

const (
	maskedValue = "********"
	// lastApplied carries the whole object as kubectl applied it, Secret values included.
	lastApplied = "kubectl.kubernetes.io/last-applied-configuration"
)

// GetYAML is the object as kubectl get -o yaml shows it, without managedFields; a Secret's values are masked unless reveal is set.
func (s *Service) GetYAML(ctx context.Context, clusterID string, kind ObjectKind, namespace, name string, reveal bool) (string, error) {
	k, err := s.clusterClient(clusterID)
	if err != nil {
		return "", err
	}
	var obj runtime.Object
	get := metav1.GetOptions{}
	switch kind {
	case ObjectPod:
		obj, err = k.client.CoreV1().Pods(namespace).Get(ctx, name, get)
	case ObjectDeployment:
		obj, err = k.client.AppsV1().Deployments(namespace).Get(ctx, name, get)
	case ObjectStatefulSet:
		obj, err = k.client.AppsV1().StatefulSets(namespace).Get(ctx, name, get)
	case ObjectDaemonSet:
		obj, err = k.client.AppsV1().DaemonSets(namespace).Get(ctx, name, get)
	case ObjectCronJob:
		obj, err = k.client.BatchV1().CronJobs(namespace).Get(ctx, name, get)
	case ObjectService:
		obj, err = k.client.CoreV1().Services(namespace).Get(ctx, name, get)
	case ObjectConfigMap:
		obj, err = k.client.CoreV1().ConfigMaps(namespace).Get(ctx, name, get)
	case ObjectSecret:
		obj, err = k.client.CoreV1().Secrets(namespace).Get(ctx, name, get)
	case ObjectIngress:
		obj, err = k.client.NetworkingV1().Ingresses(namespace).Get(ctx, name, get)
	default:
		return "", fmt.Errorf("unknown object kind %q", kind)
	}
	if err != nil {
		return "", wrapForbidden(err)
	}
	// Typed clients drop apiVersion and kind; the JSON round trip gives a map the same shape as kubectl's output to edit.
	gvks, _, err := scheme.Scheme.ObjectKinds(obj)
	if err != nil {
		return "", err
	}
	raw, err := json.Marshal(obj)
	if err != nil {
		return "", err
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return "", err
	}
	m["apiVersion"], m["kind"] = gvks[0].GroupVersion().String(), gvks[0].Kind
	meta, _ := m["metadata"].(map[string]any)
	delete(meta, "managedFields")
	if kind == ObjectSecret && !reveal {
		if data, ok := m["data"].(map[string]any); ok {
			for key := range data {
				data[key] = maskedValue
			}
		}
		if ann, ok := meta["annotations"].(map[string]any); ok && ann[lastApplied] != nil {
			ann[lastApplied] = maskedValue
		}
	}
	out, err := yaml.Marshal(m)
	return string(out), err
}
