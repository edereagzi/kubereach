package service_test

import (
	"context"
	"errors"
	"testing"

	"github.com/edereagzi/kubereach/internal/service"
	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestConfigMapsAndSecrets_ListedWithoutValuesDecodedOnGet(t *testing.T) {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "app-config"},
		Data:       map[string]string{"LOG_LEVEL": "debug", "config.yaml": "a: 1\n"},
		BinaryData: map[string][]byte{"logo.png": {0x89, 'P', 'N', 'G'}},
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "db"},
		Type:       corev1.SecretTypeOpaque,
		Data:       map[string][]byte{"password": []byte("hunter2"), "cert": {0xff, 0xfe}},
	}
	svc, cs, id := newFakeService(t, cm, secret, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "apps", Name: "empty"}})
	ctx := context.Background()

	got, err := svc.ListConfigMaps(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	want := []service.KubeConfigObject{
		{Namespace: "apps", Name: "empty"},
		{Namespace: "default", Name: "app-config", Keys: []string{"LOG_LEVEL", "config.yaml", "logo.png"}},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("configmaps (-want +got):\n%s", diff)
	}

	secrets, err := svc.ListSecrets(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]service.KubeConfigObject{{Namespace: "default", Name: "db", Type: "Opaque", Keys: []string{"cert", "password"}}}, secrets); diff != "" {
		t.Errorf("secrets (-want +got):\n%s", diff)
	}

	one, err := svc.GetConfigMap(ctx, id, "default", "app-config")
	if err != nil {
		t.Fatal(err)
	}
	want[1].Data = map[string]string{"LOG_LEVEL": "debug", "config.yaml": "a: 1\n", "logo.png": "(binary, 4 bytes)"}
	if diff := cmp.Diff(want[1], one); diff != "" {
		t.Errorf("configmap (-want +got):\n%s", diff)
	}

	s, err := svc.GetSecret(ctx, id, "default", "db")
	if err != nil {
		t.Fatal(err)
	}
	secrets[0].Data = map[string]string{"password": "hunter2", "cert": "(binary, 2 bytes)"}
	if diff := cmp.Diff(secrets[0], s); diff != "" {
		t.Errorf("secret (-want +got):\n%s", diff)
	}
	if _, err := svc.GetSecret(ctx, id, "default", "missing"); err == nil {
		t.Error("missing secret read without error")
	}

	// A role that reads ConfigMaps but not Secrets still gets the ConfigMaps.
	forbid(cs, "list", "secrets", false)
	if _, err := svc.ListSecrets(ctx, id); !errors.Is(err, service.ErrForbidden) {
		t.Errorf("forbidden secrets err = %v; want ErrForbidden", err)
	}
	if _, err := svc.ListConfigMaps(ctx, id); err != nil {
		t.Errorf("configmaps after forbidding secrets: %v", err)
	}
}
