package service_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/edereagzi/kubereach/internal/service"
	"github.com/google/go-cmp/cmp"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

const restartedAt = "kubectl.kubernetes.io/restartedAt"

func TestRestartWorkload(t *testing.T) {
	ss := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Namespace: "db", Name: "postgres"}}
	ds := &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Namespace: "kube-system", Name: "node-exporter"}}
	svc, cs, id := newFakeService(t, deployment("default", "api"), ss, ds)
	ctx := context.Background()

	for _, w := range []struct {
		kind      service.WorkloadKind
		namespace string
		name      string
		template  func() (*corev1.PodTemplateSpec, error)
	}{
		{service.WorkloadDeployment, "default", "api", func() (*corev1.PodTemplateSpec, error) {
			d, err := cs.AppsV1().Deployments("default").Get(ctx, "api", metav1.GetOptions{})
			return &d.Spec.Template, err
		}},
		{service.WorkloadStatefulSet, "db", "postgres", func() (*corev1.PodTemplateSpec, error) {
			s, err := cs.AppsV1().StatefulSets("db").Get(ctx, "postgres", metav1.GetOptions{})
			return &s.Spec.Template, err
		}},
		{service.WorkloadDaemonSet, "kube-system", "node-exporter", func() (*corev1.PodTemplateSpec, error) {
			d, err := cs.AppsV1().DaemonSets("kube-system").Get(ctx, "node-exporter", metav1.GetOptions{})
			return &d.Spec.Template, err
		}},
	} {
		if err := svc.RestartWorkload(ctx, id, w.kind, w.namespace, w.name); err != nil {
			t.Fatalf("restart %s: %v", w.kind, err)
		}
		tpl, err := w.template()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := time.Parse(time.RFC3339, tpl.Annotations[restartedAt]); err != nil {
			t.Errorf("%s restartedAt = %q; want an RFC 3339 time", w.kind, tpl.Annotations[restartedAt])
		}
	}
	if d, _ := cs.AppsV1().Deployments("default").Get(ctx, "api", metav1.GetOptions{}); len(d.Spec.Template.Spec.Containers) != 2 {
		t.Errorf("restart replaced the pod spec: %+v", d.Spec.Template.Spec)
	}

	if err := svc.RestartWorkload(ctx, id, service.WorkloadCronJob, "default", "backup"); err == nil {
		t.Error("CronJob restarted without error")
	}
	forbid(cs, "patch", "deployments", false)
	if err := svc.RestartWorkload(ctx, id, service.WorkloadDeployment, "default", "api"); !errors.Is(err, service.ErrForbidden) {
		t.Errorf("forbidden restart err = %v; want ErrForbidden", err)
	}
}

func TestDeletePod(t *testing.T) {
	svc, cs, id := newFakeService(t, pod("default", "api-1", "api", corev1.PodRunning, true, diagEpoch))
	ctx := context.Background()

	if err := svc.DeletePod(ctx, id, "default", "api-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := cs.CoreV1().Pods("default").Get(ctx, "api-1", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Errorf("pod still there: %v", err)
	}
	forbid(cs, "delete", "pods", false)
	if err := svc.DeletePod(ctx, id, "default", "api-1"); !errors.Is(err, service.ErrForbidden) {
		t.Errorf("forbidden delete err = %v; want ErrForbidden", err)
	}
}

func TestScaleWorkload(t *testing.T) {
	three := int32(3)
	ss := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Namespace: "db", Name: "postgres"}, Spec: appsv1.StatefulSetSpec{Replicas: &three}}
	svc, cs, id := newFakeService(t, deployment("default", "api"), ss)
	ctx := context.Background()

	if err := svc.ScaleWorkload(ctx, id, service.WorkloadDeployment, "default", "api", 4); err != nil {
		t.Fatal(err)
	}
	if d, _ := cs.AppsV1().Deployments("default").Get(ctx, "api", metav1.GetOptions{}); d.Spec.Replicas == nil || *d.Spec.Replicas != 4 {
		t.Errorf("deployment replicas = %v; want 4", d.Spec.Replicas)
	}
	if err := svc.ScaleWorkload(ctx, id, service.WorkloadStatefulSet, "db", "postgres", 0); err != nil {
		t.Fatal(err)
	}
	if s, _ := cs.AppsV1().StatefulSets("db").Get(ctx, "postgres", metav1.GetOptions{}); s.Spec.Replicas == nil || *s.Spec.Replicas != 0 {
		t.Errorf("statefulset replicas = %v; want 0", s.Spec.Replicas)
	}

	if err := svc.ScaleWorkload(ctx, id, service.WorkloadDaemonSet, "kube-system", "node-exporter", 2); err == nil {
		t.Error("DaemonSet scaled without error")
	}
	if err := svc.ScaleWorkload(ctx, id, service.WorkloadDeployment, "default", "api", -1); err == nil {
		t.Error("scaled to a negative count without error")
	}
	forbid(cs, "patch", "deployments", false)
	if err := svc.ScaleWorkload(ctx, id, service.WorkloadDeployment, "default", "api", 1); !errors.Is(err, service.ErrForbidden) {
		t.Errorf("forbidden scale err = %v; want ErrForbidden", err)
	}
}

func revisionReplicaSet(d *appsv1.Deployment, revision, image string, owned bool) *appsv1.ReplicaSet {
	rs := &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{Namespace: d.Namespace, Name: d.Name + "-" + revision, Labels: map[string]string{"app": d.Name}, Annotations: map[string]string{"deployment.kubernetes.io/revision": revision}},
		Spec: appsv1.ReplicaSetSpec{Template: corev1.PodTemplateSpec{
			ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": d.Name, "pod-template-hash": "h" + revision}},
			Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: image}}},
		}},
	}
	if owned {
		yes := true
		rs.OwnerReferences = []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: d.Name, UID: d.UID, Controller: &yes}}
	}
	return rs
}

func TestRollbackDeployment(t *testing.T) {
	d := deployment("default", "api")
	d.UID = types.UID("api-uid")
	d.Annotations = map[string]string{"deployment.kubernetes.io/revision": "10"}
	d.Spec.Template = corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "api"}},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "acme/api:v10"}}},
	}
	// Revision 9 belongs to another Deployment with the same labels, so 8 is the previous one.
	stranger := revisionReplicaSet(d, "9", "acme/other:v9", false)
	svc, cs, id := newFakeService(t, d,
		revisionReplicaSet(d, "10", "acme/api:v10", true),
		revisionReplicaSet(d, "8", "acme/api:v8", true),
		revisionReplicaSet(d, "2", "acme/api:v2", true),
		stranger,
	)
	ctx := context.Background()

	if err := svc.RollbackDeployment(ctx, id, "default", "api"); err != nil {
		t.Fatal(err)
	}
	got, err := cs.AppsV1().Deployments("default").Get(ctx, "api", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	want := corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "api"}},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "acme/api:v8"}}},
	}
	if diff := cmp.Diff(want, got.Spec.Template); diff != "" {
		t.Errorf("template (-want +got):\n%s", diff)
	}
}

func TestRollbackDeployment_NoPreviousRevision(t *testing.T) {
	d := deployment("default", "api")
	d.UID = types.UID("api-uid")
	d.Annotations = map[string]string{"deployment.kubernetes.io/revision": "1"}
	svc, cs, id := newFakeService(t, d, revisionReplicaSet(d, "1", "acme/api:v1", true))
	ctx := context.Background()

	err := svc.RollbackDeployment(ctx, id, "default", "api")
	if err == nil || err.Error() != `deployment "api" has no previous revision to roll back to` {
		t.Errorf("err = %v; want no previous revision", err)
	}
	forbid(cs, "list", "replicasets", false)
	if err := svc.RollbackDeployment(ctx, id, "default", "api"); !errors.Is(err, service.ErrForbidden) {
		t.Errorf("forbidden rollback err = %v; want ErrForbidden", err)
	}
}

func TestRollbackDeployment_PatchForbidden(t *testing.T) {
	d := deployment("default", "api")
	d.UID = types.UID("api-uid")
	svc, cs, id := newFakeService(t, d, revisionReplicaSet(d, "2", "acme/api:v2", true), revisionReplicaSet(d, "1", "acme/api:v1", true))
	forbid(cs, "patch", "deployments", false)
	if err := svc.RollbackDeployment(context.Background(), id, "default", "api"); !errors.Is(err, service.ErrForbidden) {
		t.Errorf("forbidden rollback patch err = %v; want ErrForbidden", err)
	}
}
