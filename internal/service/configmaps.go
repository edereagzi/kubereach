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
	"k8s.io/client-go/metadata"
)

// KubeConfigObject is a ConfigMap or a Secret. A ConfigMap row carries its keys and a Secret row its name only; Data, and a
// Secret's type and keys, are filled by GetConfigMap and GetSecret. Nothing here is logged or written to disk.
type KubeConfigObject struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	// Type is an opened Secret's type.
	Type string            `json:"type,omitempty"`
	Keys []string          `json:"keys"`
	Data map[string]string `json:"data,omitempty"`
}

func (s *Service) ListConfigMaps(ctx context.Context, clusterID string) ([]KubeConfigObject, error) {
	k, err := s.clusterClient(clusterID)
	if err != nil {
		return nil, err
	}
	cms, err := cached(ctx, k, "configmaps", []string{"configmaps"}, k.scope(), &corev1.ConfigMap{}, func(ns string) listWatcher[*corev1.ConfigMapList] { return k.client.CoreV1().ConfigMaps(ns) })
	if err != nil {
		return nil, err
	}
	out := make([]KubeConfigObject, 0, len(cms))
	for _, cm := range cms {
		out = append(out, configMapObject(cm, false))
	}
	sortConfigObjects(out)
	return out, nil
}

// ListSecrets watches Secrets as metadata only, so no value crosses the Route until one is opened.
func (s *Service) ListSecrets(ctx context.Context, clusterID string) ([]KubeConfigObject, error) {
	k, err := s.clusterClient(clusterID)
	if err != nil {
		return nil, err
	}
	meta, err := metadata.NewForConfigAndClient(k.config, k.http)
	if err != nil {
		return nil, err
	}
	secrets := meta.Resource(corev1.SchemeGroupVersion.WithResource("secrets"))
	objs, err := cached(ctx, k, "secrets", []string{"secrets"}, k.scope(), &metav1.PartialObjectMetadata{}, func(ns string) listWatcher[*metav1.PartialObjectMetadataList] { return secrets.Namespace(ns) })
	if err != nil {
		return nil, err
	}
	out := make([]KubeConfigObject, 0, len(objs))
	for _, o := range objs {
		out = append(out, KubeConfigObject{Namespace: o.Namespace, Name: o.Name})
	}
	sortConfigObjects(out)
	return out, nil
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
	return secretObject(sec), nil
}

func sortConfigObjects(out []KubeConfigObject) {
	slices.SortFunc(out, func(a, b KubeConfigObject) int {
		return cmp.Or(cmp.Compare(a.Namespace, b.Namespace), cmp.Compare(a.Name, b.Name))
	})
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

func secretObject(sec *corev1.Secret) KubeConfigObject {
	o := KubeConfigObject{Namespace: sec.Namespace, Name: sec.Name, Type: string(sec.Type), Keys: slices.Sorted(maps.Keys(sec.Data)), Data: make(map[string]string, len(sec.Data))}
	for k, v := range sec.Data {
		o.Data[k] = textValue(v)
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
