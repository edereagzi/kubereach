package service_test

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/edereagzi/kubereach/internal/service"
	"github.com/google/go-cmp/cmp"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
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

// playAPIServer makes the fake tracker's updates of resource act as the API server's: a dry run stores nothing, and a
// stale resourceVersion is a conflict. sent receives each object as it was sent.
func playAPIServer(cs *fake.Clientset, gvr schema.GroupVersionResource, sent func(runtime.Object)) {
	cs.PrependReactor("update", gvr.Resource, func(a k8stesting.Action) (bool, runtime.Object, error) {
		u := a.(k8stesting.UpdateActionImpl)
		obj := u.GetObject().DeepCopyObject()
		sent(obj)
		m, _ := meta.Accessor(obj)
		stored, err := cs.Tracker().Get(gvr, m.GetNamespace(), m.GetName())
		if err != nil {
			return true, nil, err
		}
		sm, _ := meta.Accessor(stored)
		if m.GetResourceVersion() != sm.GetResourceVersion() {
			return true, nil, apierrors.NewConflict(gvr.GroupResource(), m.GetName(), errors.New("the object has been modified"))
		}
		if len(u.UpdateOptions.DryRun) > 0 {
			return true, obj, nil
		}
		rv, _ := strconv.Atoi(sm.GetResourceVersion())
		m.SetResourceVersion(strconv.Itoa(rv + 1))
		return true, obj, cs.Tracker().Update(gvr, obj, m.GetNamespace())
	})
}

func TestEditYAML_DiffsAgainstDryRunThenUpdates(t *testing.T) {
	d := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "api", ResourceVersion: "5",
			ManagedFields: []metav1.ManagedFieldsEntry{{Manager: "kubectl", Operation: metav1.ManagedFieldsOperationApply}}},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "api"}},
			Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "acme/api:v1"}}}},
		},
		Status: appsv1.DeploymentStatus{Replicas: 3, ReadyReplicas: 3},
	}
	svc, cs, id := newFakeService(t, d)
	gvr := appsv1.SchemeGroupVersion.WithResource("deployments")
	var sent []*appsv1.Deployment
	playAPIServer(cs, gvr, func(o runtime.Object) { sent = append(sent, o.(*appsv1.Deployment)) })
	stored := func() *appsv1.Deployment {
		got, err := cs.Tracker().Get(gvr, "default", "api")
		if err != nil {
			t.Fatal(err)
		}
		return got.(*appsv1.Deployment)
	}
	ctx := context.Background()

	text, err := svc.EditYAML(ctx, id, service.ObjectDeployment, "default", "api")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"kind: Deployment\n", "  resourceVersion: \"5\"\n", "      - image: acme/api:v1\n"} {
		if !strings.Contains(text, want) {
			t.Errorf("edit yaml lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "managedFields") || strings.Contains(text, "\nstatus:") {
		t.Errorf("edit yaml shows managedFields or status:\n%s", text)
	}

	// A status typed into the editor is not sent.
	edited := strings.Replace(text, "acme/api:v1", "acme/api:v2", 1) + "status:\n  replicas: 9\n"
	diff, err := svc.DiffYAML(ctx, id, service.ObjectDeployment, "default", "api", text, edited)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(diff, "-      - image: acme/api:v1\n+      - image: acme/api:v2\n") || strings.Contains(diff, "resourceVersion") {
		t.Errorf("diff:\n%s", diff)
	}
	if img := stored().Spec.Template.Spec.Containers[0].Image; img != "acme/api:v1" {
		t.Errorf("image after the dry run = %s, want it unchanged", img)
	}

	// The controller writes status meanwhile, which the edit does not overwrite.
	busy := stored()
	busy.ResourceVersion, busy.Status.ReadyReplicas = "6", 2
	if err := cs.Tracker().Update(gvr, busy, "default"); err != nil {
		t.Fatal(err)
	}
	if err := svc.ApplyYAML(ctx, id, service.ObjectDeployment, "default", "api", text, edited); err != nil {
		t.Fatal(err)
	}
	if img := stored().Spec.Template.Spec.Containers[0].Image; img != "acme/api:v2" {
		t.Errorf("image after apply = %s, want acme/api:v2", img)
	}
	if len(sent) != 2 || slices.ContainsFunc(sent, func(d *appsv1.Deployment) bool { return d.Status.Replicas != 0 }) {
		t.Errorf("sent %d updates, want 2 without status", len(sent))
	}

	// The image the editor loaded is not the Deployment's any more.
	for _, try := range []func() error{
		func() error {
			_, err := svc.DiffYAML(ctx, id, service.ObjectDeployment, "default", "api", text, edited)
			return err
		},
		func() error { return svc.ApplyYAML(ctx, id, service.ObjectDeployment, "default", "api", text, edited) },
	} {
		if err := try(); service.Describe(err).Code != "conflict" {
			t.Errorf("stale edit: err = %v, want a conflict", err)
		}
	}

	for name, edit := range map[string]string{
		"invalid": "spec: [",
		"unknown": strings.Replace(text, "image:", "imagee:", 1),
		"renamed": strings.Replace(text, "name: api", "name: web", 1),
		"kind":    strings.Replace(text, "kind: Deployment", "kind: StatefulSet", 1),
	} {
		if _, err := svc.DiffYAML(ctx, id, service.ObjectDeployment, "default", "api", text, edit); err == nil || service.Describe(err).Message == "Something went wrong" {
			t.Errorf("%s: err = %v, want it explained", name, err)
		}
	}
}

func TestEditYAML_KindWithoutStatus(t *testing.T) {
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "app"}, Data: map[string]string{"mode": "fast"}}
	svc, _, id := newFakeService(t, cm)
	ctx := context.Background()
	text, err := svc.EditYAML(ctx, id, service.ObjectConfigMap, "default", "app")
	if err != nil {
		t.Fatal(err)
	}
	edited := strings.Replace(text, "mode: fast", "mode: slow", 1)
	if err := svc.ApplyYAML(ctx, id, service.ObjectConfigMap, "default", "app", text, edited); err != nil {
		t.Fatal(err)
	}
	if got, _ := svc.EditYAML(ctx, id, service.ObjectConfigMap, "default", "app"); !strings.Contains(got, "mode: slow") {
		t.Errorf("configmap after apply:\n%s", got)
	}
}

func TestEditYAML_SecretValuesDecodedAndEncodedBack(t *testing.T) {
	values := map[string][]byte{
		"password": []byte("hunter2"),
		"cert":     []byte("-----BEGIN-----\n  indented: yes\n-----END-----\n"),
		"empty":    {},
		"awkward":  []byte("nul\x00 tab\t crlf\r\n trailing  "),
		"blob":     {0xff, 0x00, 0x01},
		// UTF-8 that YAML cannot write as text.
		"del":     []byte("a\x7fb"),
		"nonchar": []byte("\uFFFE"),
	}
	sec := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "db"}, Type: corev1.SecretTypeOpaque, Data: values}
	svc, cs, id := newFakeService(t, sec)
	gvr := corev1.SchemeGroupVersion.WithResource("secrets")
	playAPIServer(cs, gvr, func(runtime.Object) {})
	stored := func() map[string][]byte {
		got, err := cs.Tracker().Get(gvr, "default", "db")
		if err != nil {
			t.Fatal(err)
		}
		return got.(*corev1.Secret).Data
	}
	ctx := context.Background()

	text, err := svc.EditYAML(ctx, id, service.ObjectSecret, "default", "db")
	if err != nil {
		t.Fatal(err)
	}
	// Text is shown decoded; the binary value stays base64 under data.
	for _, want := range []string{"# Values under data are not text", "data:\n  blob: /wAB\n", "  del: YX9i\n", "stringData:\n", "  password: hunter2\n", "  empty: \"\"\n"} {
		if !strings.Contains(text, want) {
			t.Errorf("secret edit yaml lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "aHVudGVyMg==") {
		t.Errorf("secret edit yaml shows an encoded text value:\n%s", text)
	}

	// Unchanged text round-trips every value byte for byte.
	if err := svc.ApplyYAML(ctx, id, service.ObjectSecret, "default", "db", text, text); err != nil {
		t.Fatal(err)
	}
	if got := stored(); !cmp.Equal(got, values) {
		t.Errorf("after an unchanged apply: %s", cmp.Diff(values, got))
	}

	edited := strings.Replace(text, "password: hunter2", "password: hunter3", 1)
	diff, err := svc.DiffYAML(ctx, id, service.ObjectSecret, "default", "db", text, edited)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(diff, "-  password: hunter2\n+  password: hunter3\n") {
		t.Errorf("diff does not show decoded values:\n%s", diff)
	}
	if err := svc.ApplyYAML(ctx, id, service.ObjectSecret, "default", "db", text, edited); err != nil {
		t.Fatal(err)
	}
	want := maps.Clone(values)
	want["password"] = []byte("hunter3")
	if got := stored(); !cmp.Equal(got, want) {
		t.Errorf("after changing one key: %s", cmp.Diff(want, got))
	}
	// The password the editor loaded is not the Secret's any more.
	if err := svc.ApplyYAML(ctx, id, service.ObjectSecret, "default", "db", text, edited); service.Describe(err).Code != "conflict" {
		t.Errorf("stale secret edit: err = %v, want a conflict", err)
	}
}
