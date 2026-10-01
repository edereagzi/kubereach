package service_test

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/edereagzi/kubereach/internal/service"
	"github.com/google/go-cmp/cmp"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	k8stesting "k8s.io/client-go/testing"
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

func TestDeleteObject(t *testing.T) {
	meta := func(name string) metav1.ObjectMeta { return metav1.ObjectMeta{Namespace: "default", Name: name} }
	objects := map[service.ObjectKind]runtime.Object{
		service.ObjectPod:         pod("default", "api-1", "api", corev1.PodRunning, true, diagEpoch),
		service.ObjectDeployment:  deployment("default", "api"),
		service.ObjectStatefulSet: &appsv1.StatefulSet{ObjectMeta: meta("db")},
		service.ObjectDaemonSet:   &appsv1.DaemonSet{ObjectMeta: meta("agent")},
		service.ObjectJob:         &batchv1.Job{ObjectMeta: meta("migrate")},
		service.ObjectCronJob:     &batchv1.CronJob{ObjectMeta: meta("backup")},
		service.ObjectService:     k8sService("default", "web", 80),
		service.ObjectIngress:     &networkingv1.Ingress{ObjectMeta: meta("web")},
		service.ObjectConfigMap:   &corev1.ConfigMap{ObjectMeta: meta("settings")},
		service.ObjectSecret:      &corev1.Secret{ObjectMeta: meta("token")},
	}
	pvc := &corev1.PersistentVolumeClaim{ObjectMeta: meta("data")}
	svc, cs, id := newFakeService(t, append(slices.Collect(maps.Values(objects)), pvc)...)
	ctx := context.Background()

	// kubectl deletes a Job's or CronJob's pods with it; the API default for batch/v1 would orphan them.
	policies := map[string]metav1.DeletionPropagation{}
	cs.PrependReactor("delete", "*", func(a k8stesting.Action) (bool, runtime.Object, error) {
		if p := a.(k8stesting.DeleteActionImpl).DeleteOptions.PropagationPolicy; p != nil {
			policies[a.GetResource().Resource] = *p
		}
		return false, nil, nil
	})
	for kind, obj := range objects {
		name := obj.(metav1.Object).GetName()
		if err := svc.DeleteObject(ctx, id, kind, "default", name); err != nil {
			t.Fatalf("delete %s: %v", kind, err)
		}
		if _, err := svc.GetYAML(ctx, id, kind, "default", name, false); !apierrors.IsNotFound(err) {
			t.Errorf("%s %s still there: %v", kind, name, err)
		}
	}
	for _, r := range []string{"jobs", "cronjobs"} {
		if policies[r] != metav1.DeletePropagationBackground {
			t.Errorf("%s propagation = %q; want Background", r, policies[r])
		}
	}

	if err := svc.DeleteObject(ctx, id, service.ObjectPVC, "default", "data"); err == nil {
		t.Error("PVC deleted without error")
	}
	if _, err := cs.CoreV1().PersistentVolumeClaims("default").Get(ctx, "data", metav1.GetOptions{}); err != nil {
		t.Errorf("PVC gone: %v", err)
	}
	if err := cs.Tracker().Add(deployment("default", "api")); err != nil {
		t.Fatal(err)
	}
	forbid(cs, "delete", "deployments", false)
	if err := svc.DeleteObject(ctx, id, service.ObjectDeployment, "default", "api"); !errors.Is(err, service.ErrForbidden) {
		t.Errorf("forbidden delete err = %v; want ErrForbidden", err)
	}
}

func cronJob(ns, name string) *batchv1.CronJob {
	return &batchv1.CronJob{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, UID: types.UID(name + "-uid")},
		Spec: batchv1.CronJobSpec{
			Schedule: "0 3 * * *",
			JobTemplate: batchv1.JobTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "backup"}},
				Spec: batchv1.JobSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyNever,
					Containers:    []corev1.Container{{Name: "dump", Image: "pg:17"}},
				}}},
			},
		},
	}
}

func TestRunCronJob(t *testing.T) {
	long := strings.Repeat("n", 52)
	svc, cs, id := newFakeService(t, cronJob("db", "backup"), cronJob("db", long))
	ctx := context.Background()

	if err := svc.RunCronJob(ctx, id, "db", "backup"); err != nil {
		t.Fatal(err)
	}
	if err := svc.RunCronJob(ctx, id, "db", long); err != nil {
		t.Fatal(err)
	}
	jobs, _ := cs.BatchV1().Jobs("db").List(ctx, metav1.ListOptions{})
	if len(jobs.Items) != 2 {
		t.Fatalf("jobs = %d; want 2", len(jobs.Items))
	}
	for _, j := range jobs.Items {
		// As kubectl does, the owner does not block the CronJob's deletion, which would need RBAC on cronjobs/finalizers.
		owner := metav1.GetControllerOf(&j)
		if owner == nil || owner.Kind != "CronJob" || !strings.HasPrefix(j.Name, owner.Name[:min(len(owner.Name), 50)]) || owner.UID != types.UID(owner.Name+"-uid") || owner.BlockOwnerDeletion != nil {
			t.Errorf("%s controller = %+v; want its CronJob, not blocking deletion", j.Name, owner)
		}
		if !strings.Contains(j.Name, "-manual-") || len(j.Name) > 63 {
			t.Errorf("job name = %q; want <cronjob>-manual-<suffix>, at most 63 characters", j.Name)
		}
		if j.Annotations["cronjob.kubernetes.io/instantiate"] != "manual" || j.Labels["app"] != "backup" || j.Spec.Template.Spec.Containers[0].Image != "pg:17" {
			t.Errorf("job %s is not the CronJob's template: %+v", j.Name, j)
		}
	}
	if !slices.ContainsFunc(jobs.Items, func(j batchv1.Job) bool { return strings.HasPrefix(j.Name, "backup-manual-") }) {
		t.Errorf("no job named backup-manual-*: %+v", jobs.Items)
	}

	if err := svc.RunCronJob(ctx, id, "db", "missing"); err == nil {
		t.Error("ran a missing CronJob without error")
	}
	forbid(cs, "create", "jobs", false)
	if err := svc.RunCronJob(ctx, id, "db", "backup"); !errors.Is(err, service.ErrForbidden) {
		t.Errorf("forbidden run err = %v; want ErrForbidden", err)
	}
}

func TestSuspendCronJob(t *testing.T) {
	svc, cs, id := newFakeService(t, cronJob("db", "backup"))
	ctx := context.Background()
	suspended := func() bool {
		cj, err := cs.BatchV1().CronJobs("db").Get(ctx, "backup", metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		return cj.Spec.Suspend != nil && *cj.Spec.Suspend
	}

	if err := svc.SuspendCronJob(ctx, id, "db", "backup", true); err != nil {
		t.Fatal(err)
	}
	if !suspended() {
		t.Error("CronJob not suspended")
	}
	if err := svc.SuspendCronJob(ctx, id, "db", "backup", false); err != nil {
		t.Fatal(err)
	}
	if suspended() {
		t.Error("CronJob still suspended after resume")
	}
	forbid(cs, "patch", "cronjobs", false)
	if err := svc.SuspendCronJob(ctx, id, "db", "backup", true); !errors.Is(err, service.ErrForbidden) {
		t.Errorf("forbidden suspend err = %v; want ErrForbidden", err)
	}
}

func TestCordonNode(t *testing.T) {
	svc, cs, id := newFakeService(t, node("node-a", nodeCondition(corev1.NodeReady, corev1.ConditionTrue)))
	ctx := context.Background()
	// The list is what shows a cordoned node, so the toggle is read back through it.
	cordoned := func() bool {
		nodes, err := svc.ListNodes(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return nodes[0].Unschedulable
	}

	if err := svc.CordonNode(ctx, id, "node-a", true); err != nil {
		t.Fatal(err)
	}
	if !cordoned() {
		t.Error("node not cordoned")
	}
	if err := svc.CordonNode(ctx, id, "node-a", false); err != nil {
		t.Fatal(err)
	}
	if cordoned() {
		t.Error("node still cordoned after uncordon")
	}
	forbid(cs, "patch", "nodes", false)
	if err := svc.CordonNode(ctx, id, "node-a", true); !errors.Is(err, service.ErrForbidden) {
		t.Errorf("forbidden cordon err = %v; want ErrForbidden", err)
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
	if err == nil || service.Describe(err).Message != "Deployment api has no previous revision to roll back to" {
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

func TestRollbackDeployment_ConcurrentChangeConflicts(t *testing.T) {
	d := deployment("default", "api")
	d.UID = types.UID("api-uid")
	d.ResourceVersion = "8"
	d.Spec.Template.Spec.Containers[0].Image = "acme/api:v2"
	svc, cs, id := newFakeService(t, d, revisionReplicaSet(d, "2", "acme/api:v2", true), revisionReplicaSet(d, "1", "acme/api:v1", true))
	// The Deployment is changed right after it is read; the fake tracker keeps no resourceVersions, so the
	// API server's optimistic lock on a patch that carries one is played here.
	serverVersion := "8"
	cs.PrependReactor("get", "deployments", func(k8stesting.Action) (bool, runtime.Object, error) {
		stale := d.DeepCopy()
		serverVersion = "9"
		return true, stale, nil
	})
	cs.PrependReactor("patch", "deployments", func(a k8stesting.Action) (bool, runtime.Object, error) {
		var ops []struct{ Path, Value any }
		if err := json.Unmarshal(a.(k8stesting.PatchAction).GetPatch(), &ops); err != nil {
			return true, nil, err
		}
		for _, op := range ops {
			if op.Path == "/metadata/resourceVersion" && op.Value != serverVersion {
				return true, nil, apierrors.NewConflict(schema.GroupResource{Group: "apps", Resource: "deployments"}, "api", errors.New("the object has been modified"))
			}
		}
		return false, nil, nil
	})
	ctx := context.Background()

	err := svc.RollbackDeployment(ctx, id, "default", "api")
	if !apierrors.IsConflict(err) || service.Describe(err).Message != "It changed in the Cluster meanwhile; try again" {
		t.Errorf("err = %v; want a conflict", err)
	}
	got, err := cs.Tracker().Get(appsv1.SchemeGroupVersion.WithResource("deployments"), "default", "api")
	if err != nil {
		t.Fatal(err)
	}
	if image := got.(*appsv1.Deployment).Spec.Template.Spec.Containers[0].Image; image != "acme/api:v2" {
		t.Errorf("image after a conflicting rollback = %s, want it unchanged", image)
	}
}
