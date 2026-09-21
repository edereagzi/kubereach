package service_test

import (
	"context"
	"strings"
	"testing"

	"github.com/edereagzi/kubereach/internal/service"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestGetYAML_StripsManagedFieldsAndMasksSecrets(t *testing.T) {
	managed := []metav1.ManagedFieldsEntry{{Manager: "kubectl", Operation: metav1.ManagedFieldsOperationApply}}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default", Name: "db", ManagedFields: managed,
			Annotations: map[string]string{"kubectl.kubernetes.io/last-applied-configuration": `{"data":{"password":"aHVudGVyMg=="}}`},
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{"password": []byte("hunter2")},
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "web", ManagedFields: managed},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "nginx:1.27"}}},
	}
	svc, _, id := newFakeService(t, secret, pod)
	ctx := context.Background()

	masked, err := svc.GetYAML(ctx, id, service.ObjectSecret, "default", "db", false)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"apiVersion: v1\n", "kind: Secret\n", "  password: '********'\n", "kubectl.kubernetes.io/last-applied-configuration: '********'\n", "type: Opaque\n"} {
		if !strings.Contains(masked, want) {
			t.Errorf("masked secret yaml lacks %q:\n%s", want, masked)
		}
	}
	for _, leak := range []string{"managedFields", "hunter2", "aHVudGVyMg=="} {
		if strings.Contains(masked, leak) {
			t.Errorf("masked secret yaml contains %q:\n%s", leak, masked)
		}
	}

	revealed, err := svc.GetYAML(ctx, id, service.ObjectSecret, "default", "db", true)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(revealed, "aHVudGVyMg==") != 2 || strings.Contains(revealed, "managedFields") {
		t.Errorf("revealed secret yaml:\n%s", revealed)
	}

	got, err := svc.GetYAML(ctx, id, service.ObjectPod, "default", "web", false)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"kind: Pod\n", "  - image: nginx:1.27\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("pod yaml lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "managedFields") {
		t.Errorf("pod yaml keeps managedFields:\n%s", got)
	}

	if _, err := svc.GetYAML(ctx, id, service.ObjectPod, "default", "missing", false); err == nil {
		t.Error("missing pod read without error")
	}
	if _, err := svc.GetYAML(ctx, id, "volume", "default", "web", false); err == nil {
		t.Error("unknown kind read without error")
	}
}
