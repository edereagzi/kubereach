package service

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"slices"
	"unicode/utf8"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// KubeConfigObject is a ConfigMap or a Secret. Lists carry the keys only; Data is filled by GetConfigMap and GetSecret,
// so a Secret's values leave the Cluster only when one is opened. Nothing here is logged or written to disk.
type KubeConfigObject struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	// Type is the Secret's type; empty for a ConfigMap.
	Type string            `json:"type,omitempty"`
	Keys []string          `json:"keys"`
	Data map[string]string `json:"data,omitempty"`
}

func (s *Service) ListConfigMaps(ctx context.Context, clusterID string) ([]KubeConfigObject, error) {
	return s.listConfigObjects(ctx, clusterID, func(k kube, ns string) ([]KubeConfigObject, error) {
		list, err := k.client.CoreV1().ConfigMaps(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, err
		}
		out := make([]KubeConfigObject, 0, len(list.Items))
		for i := range list.Items {
			out = append(out, configMapObject(&list.Items[i], false))
		}
		return out, nil
	})
}

func (s *Service) ListSecrets(ctx context.Context, clusterID string) ([]KubeConfigObject, error) {
	return s.listConfigObjects(ctx, clusterID, func(k kube, ns string) ([]KubeConfigObject, error) {
		list, err := k.client.CoreV1().Secrets(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, err
		}
		out := make([]KubeConfigObject, 0, len(list.Items))
		for i := range list.Items {
			out = append(out, secretObject(&list.Items[i], false))
		}
		return out, nil
	})
}

func (s *Service) GetConfigMap(ctx context.Context, clusterID, namespace, name string) (KubeConfigObject, error) {
	k, err := s.clusterClient(clusterID)
	if err != nil {
		return KubeConfigObject{}, err
	}
	cm, err := k.client.CoreV1().ConfigMaps(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return KubeConfigObject{}, wrapForbidden(err)
	}
	return configMapObject(cm, true), nil
}

func (s *Service) GetSecret(ctx context.Context, clusterID, namespace, name string) (KubeConfigObject, error) {
	k, err := s.clusterClient(clusterID)
	if err != nil {
		return KubeConfigObject{}, err
	}
	sec, err := k.client.CoreV1().Secrets(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return KubeConfigObject{}, wrapForbidden(err)
	}
	return secretObject(sec, true), nil
}

func (s *Service) listConfigObjects(ctx context.Context, clusterID string, list func(k kube, ns string) ([]KubeConfigObject, error)) ([]KubeConfigObject, error) {
	k, err := s.clusterClient(clusterID)
	if err != nil {
		return nil, err
	}
	var out []KubeConfigObject
	for _, ns := range k.scope() {
		items, err := list(k, ns)
		if err != nil {
			return nil, wrapForbidden(err)
		}
		out = append(out, items...)
	}
	slices.SortFunc(out, func(a, b KubeConfigObject) int {
		return cmp.Or(cmp.Compare(a.Namespace, b.Namespace), cmp.Compare(a.Name, b.Name))
	})
	return out, nil
}

func configMapObject(cm *corev1.ConfigMap, withData bool) KubeConfigObject {
	o := KubeConfigObject{Namespace: cm.Namespace, Name: cm.Name}
	o.Keys = slices.Collect(maps.Keys(cm.Data))
	for k := range cm.BinaryData {
		o.Keys = append(o.Keys, k)
	}
	slices.Sort(o.Keys)
	if withData {
		o.Data = make(map[string]string, len(o.Keys))
		maps.Copy(o.Data, cm.Data)
		for k, v := range cm.BinaryData {
			o.Data[k] = textValue(v)
		}
	}
	return o
}

func secretObject(sec *corev1.Secret, withData bool) KubeConfigObject {
	o := KubeConfigObject{Namespace: sec.Namespace, Name: sec.Name, Type: string(sec.Type), Keys: slices.Sorted(maps.Keys(sec.Data))}
	if withData {
		o.Data = make(map[string]string, len(sec.Data))
		for k, v := range sec.Data {
			o.Data[k] = textValue(v)
		}
	}
	return o
}

// textValue is the value as text, or a size note when it is not valid UTF-8 and would only render as garbage.
func textValue(b []byte) string {
	if utf8.Valid(b) {
		return string(b)
	}
	return fmt.Sprintf("(binary, %d bytes)", len(b))
}
