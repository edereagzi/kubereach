package service_test

import (
	"context"
	"testing"
	"time"

	"github.com/edereagzi/kubereach/internal/service"
	"github.com/google/go-cmp/cmp"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const envoyImage = "envoyproxy/envoy@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func rolloutDeployment(name string, revision string, desired, updated, ready, available, total int32, progressing appsv1.DeploymentCondition) *appsv1.Deployment {
	d := deployment("default", name)
	d.Annotations = map[string]string{"deployment.kubernetes.io/revision": revision}
	d.Generation, d.Spec.Replicas = 3, &desired
	d.Spec.Template.Spec.InitContainers = []corev1.Container{{Name: "migrate", Image: "ghcr.io/acme/" + name + "-migrate:v" + revision}}
	d.Spec.Template.Spec.Containers = []corev1.Container{{Name: "app", Image: "ghcr.io/acme/" + name + ":v" + revision}, {Name: "sidecar", Image: envoyImage}}
	d.Status = appsv1.DeploymentStatus{ObservedGeneration: 3, Replicas: total, UpdatedReplicas: updated, ReadyReplicas: ready, AvailableReplicas: available, Conditions: []appsv1.DeploymentCondition{progressing}}
	return d
}

func TestDescribeWorkload_Deployments(t *testing.T) {
	healthy := rolloutDeployment("api", "4", 3, 3, 3, 3, 3, appsv1.DeploymentCondition{Type: appsv1.DeploymentProgressing, Status: corev1.ConditionTrue, Reason: "NewReplicaSetAvailable", Message: `ReplicaSet "api-7d9" has successfully progressed.`})
	rolling := rolloutDeployment("web", "12", 3, 1, 3, 3, 4, appsv1.DeploymentCondition{Type: appsv1.DeploymentProgressing, Status: corev1.ConditionTrue, Reason: "ReplicaSetUpdated", Message: `ReplicaSet "web-6c1" is progressing.`})
	stuck := rolloutDeployment("worker", "7", 2, 1, 2, 2, 3, appsv1.DeploymentCondition{Type: appsv1.DeploymentProgressing, Status: corev1.ConditionFalse, Reason: "ProgressDeadlineExceeded", Message: `ReplicaSet "worker-9f2" has timed out progressing.`})
	ev := event("default", "worker", "Warning", "BackOff", "not this kind", 1, diagEpoch)
	ev.InvolvedObject.Kind = "Deployment"
	ev.Name, ev.Reason, ev.Message = "worker.scaled", "ScalingReplicaSet", "Scaled up replica set worker-9f2 to 1"
	svc, cs, id := newFakeService(t, healthy, rolling, stuck, ev, event("default", "worker", "Warning", "BackOff", "a pod, not the deployment", 1, diagEpoch),
		pod("default", "worker-9f2-a", "worker", corev1.PodRunning, true, diagEpoch), pod("default", "api-7d9-a", "api", corev1.PodRunning, true, diagEpoch))
	ctx := context.Background()

	got, err := svc.ListWorkloads(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	containers := func(name, revision string) []service.WorkloadContainer {
		return []service.WorkloadContainer{
			{Name: "migrate", Init: true, Image: "ghcr.io/acme/" + name + "-migrate:v" + revision, Tag: "v" + revision},
			{Name: "app", Image: "ghcr.io/acme/" + name + ":v" + revision, Tag: "v" + revision},
			{Name: "sidecar", Image: envoyImage, Tag: "0123456789ab"},
		}
	}
	want := []service.KubeWorkload{
		{Namespace: "default", Name: "api", Kind: service.WorkloadDeployment, Containers: containers("api", "4"), Rollout: &service.Rollout{Desired: 3, Updated: 3, Ready: 3, Available: 3, Revision: "4", State: service.RolloutComplete, Message: `ReplicaSet "api-7d9" has successfully progressed.`}},
		{Namespace: "default", Name: "web", Kind: service.WorkloadDeployment, Containers: containers("web", "12"), Rollout: &service.Rollout{Desired: 3, Updated: 1, Ready: 3, Available: 3, Revision: "12", State: service.RolloutProgressing, Message: `ReplicaSet "web-6c1" is progressing.`}},
		{Namespace: "default", Name: "worker", Kind: service.WorkloadDeployment, Containers: containers("worker", "7"), Rollout: &service.Rollout{Desired: 2, Updated: 1, Ready: 2, Available: 2, Revision: "7", State: service.RolloutStuck, Message: `ReplicaSet "worker-9f2" has timed out progressing.`}},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("workloads (-want +got):\n%s", diff)
	}

	d, err := svc.DescribeWorkload(ctx, id, service.WorkloadDeployment, "default", "worker")
	if err != nil {
		t.Fatal(err)
	}
	wantEvents := []service.KubeEvent{{ID: "default/worker.scaled", Kind: "Deployment", Namespace: "default", Name: "worker", Type: "Warning", Reason: "ScalingReplicaSet", Message: "Scaled up replica set worker-9f2 to 1", Count: 1, Time: diagEpoch}}
	wantPods := []service.KubePod{{Namespace: "default", Name: "worker-9f2-a", Containers: []string{"main"}, Ports: []service.NamedPort{{Name: "http", Port: 8080}}}}
	if diff := cmp.Diff(service.WorkloadDiagnosis{Workload: want[2], Pods: wantPods, Events: wantEvents}, d); diff != "" {
		t.Errorf("diagnosis (-want +got):\n%s", diff)
	}

	forbid(cs, "list", "events", false)
	if d, err = svc.DescribeWorkload(ctx, id, service.WorkloadDeployment, "default", "worker"); err != nil || d.Events != nil || d.EventsError == "" {
		t.Errorf("forbidden events = %+v, %v; want none with an error", d, err)
	}
	if _, err := svc.DescribeWorkload(ctx, id, service.WorkloadDeployment, "default", "missing"); err == nil {
		t.Error("missing deployment described without error")
	}
	if _, err := svc.DescribeWorkload(ctx, id, "replicaset", "default", "worker"); err == nil {
		t.Error("unknown kind described without error")
	}

	// A new rollout on a stuck Deployment is progressing until the controller has observed it.
	stuck.Generation++
	if _, err := cs.AppsV1().Deployments("default").Update(ctx, stuck, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if d, err := svc.DescribeWorkload(ctx, id, service.WorkloadDeployment, "default", "worker"); err != nil || d.Workload.Rollout.State != service.RolloutProgressing {
		t.Errorf("unobserved generation = %+v, %v; want progressing", d.Workload.Rollout, err)
	}
}

func TestListWorkloads_StatefulSetDaemonSetCronJob(t *testing.T) {
	two := int32(2)
	ss := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Namespace: "db", Name: "postgres", Generation: 2},
		Spec:       appsv1.StatefulSetSpec{Replicas: &two, Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "postgres", Image: "localhost:5000/postgres"}}}}},
		Status:     appsv1.StatefulSetStatus{ObservedGeneration: 2, Replicas: 2, ReadyReplicas: 2, UpdatedReplicas: 1, AvailableReplicas: 2, CurrentRevision: "postgres-aaa", UpdateRevision: "postgres-bbb"},
	}
	ds := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{Namespace: "kube-system", Name: "node-exporter", Annotations: map[string]string{"deprecated.daemonset.template.generation": "5"}},
		Status:     appsv1.DaemonSetStatus{DesiredNumberScheduled: 3, UpdatedNumberScheduled: 3, NumberReady: 3, NumberAvailable: 3},
	}
	suspended := true
	cj := &batchv1.CronJob{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "backup"},
		Spec:       batchv1.CronJobSpec{Schedule: "0 3 * * *", Suspend: &suspended, JobTemplate: batchv1.JobTemplateSpec{Spec: batchv1.JobSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "backup", Image: "acme/backup:2024.09@sha256:abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"}}}}}}},
		Status:     batchv1.CronJobStatus{LastScheduleTime: &metav1.Time{Time: diagEpoch}},
	}
	svc, cs, id := newFakeService(t, ss, ds, cj)

	got, err := svc.ListWorkloads(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	want := []service.KubeWorkload{
		{Namespace: "db", Name: "postgres", Kind: service.WorkloadStatefulSet, Containers: []service.WorkloadContainer{{Name: "postgres", Image: "localhost:5000/postgres", Tag: "latest"}}, Rollout: &service.Rollout{Desired: 2, Updated: 1, Ready: 2, Available: 2, Revision: "postgres-bbb", State: service.RolloutProgressing}},
		{Namespace: "default", Name: "backup", Kind: service.WorkloadCronJob, Containers: []service.WorkloadContainer{{Name: "backup", Image: "acme/backup:2024.09@sha256:abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789", Tag: "abcdef012345"}}, CronJob: &service.CronJobState{Schedule: "0 3 * * *", Suspend: true, LastScheduled: diagEpoch}},
		{Namespace: "kube-system", Name: "node-exporter", Kind: service.WorkloadDaemonSet, Rollout: &service.Rollout{Desired: 3, Updated: 3, Ready: 3, Available: 3, Revision: "5", State: service.RolloutComplete}},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("workloads (-want +got):\n%s", diff)
	}

	ss.Spec.UpdateStrategy.Type = appsv1.OnDeleteStatefulSetStrategyType
	if _, err := cs.AppsV1().StatefulSets("db").Update(context.Background(), ss, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if d, err := svc.DescribeWorkload(context.Background(), id, service.WorkloadStatefulSet, "db", "postgres"); err != nil || d.Workload.Rollout.State != service.RolloutComplete {
		t.Errorf("OnDelete statefulset = %+v, %v; want complete, nothing rolls by itself", d.Workload.Rollout, err)
	}

	d, err := svc.DescribeWorkload(context.Background(), id, service.WorkloadCronJob, "default", "backup")
	if err != nil || d.Workload.CronJob == nil || !d.Workload.CronJob.LastScheduled.Equal(diagEpoch) {
		t.Errorf("cronjob diagnosis = %+v, %v", d, err)
	}

	svc, cs, id = newFakeService(t, ss, ds, cj)
	forbid(cs, "list", "cronjobs", false)
	if got, err = svc.ListWorkloads(context.Background(), id); err != nil || len(got) != 2 {
		t.Errorf("forbidden cronjobs: workloads = %+v, %v; want the other two", got, err)
	}
}

// A Job is running until its Complete or Failed condition says otherwise; its pods are the ones its selector matches.
func TestListWorkloads_Jobs(t *testing.T) {
	yes, three := true, int32(3)
	started, ended := metav1.NewTime(diagEpoch), metav1.NewTime(diagEpoch.Add(42*time.Second))
	template := corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "report", Image: "acme/report:1.2"}}}}
	selector := &metav1.LabelSelector{MatchLabels: map[string]string{"app": "nightly-28942"}}
	running := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "nightly-28942", OwnerReferences: controlledBy("CronJob", "nightly")},
		Spec:       batchv1.JobSpec{Completions: &three, Selector: selector, Template: template},
		Status:     batchv1.JobStatus{Active: 1, Succeeded: 1, StartTime: &started},
	}
	done := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "migrate", OwnerReferences: []metav1.OwnerReference{{Kind: "Deployment", Name: "not-its-controller"}}},
		Spec:       batchv1.JobSpec{Template: template},
		Status: batchv1.JobStatus{Succeeded: 1, StartTime: &started, CompletionTime: &ended,
			Conditions: []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}}},
	}
	failed := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "seed"},
		Spec:       batchv1.JobSpec{Suspend: &yes, Template: template},
		Status: batchv1.JobStatus{Failed: 6, StartTime: &started,
			Conditions: []batchv1.JobCondition{{Type: batchv1.JobFailed, Status: corev1.ConditionTrue, Reason: "BackoffLimitExceeded", Message: "Job has reached the specified backoff limit", LastTransitionTime: ended}}},
	}
	svc, cs, id := newFakeService(t, running, done, failed,
		pod("default", "nightly-28942-a", "nightly-28942", corev1.PodRunning, true, diagEpoch), pod("default", "api-1", "api", corev1.PodRunning, true, diagEpoch))

	got, err := svc.ListWorkloads(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	containers := []service.WorkloadContainer{{Name: "report", Image: "acme/report:1.2", Tag: "1.2"}}
	want := []service.KubeWorkload{
		{Namespace: "default", Name: "migrate", Kind: service.WorkloadJob, Containers: containers, Job: &service.JobState{Result: service.JobComplete, Completions: 1, Succeeded: 1, StartedAt: diagEpoch, FinishedAt: ended.Time}},
		{Namespace: "default", Name: "nightly-28942", Kind: service.WorkloadJob, Containers: containers, Job: &service.JobState{Result: service.JobRunning, Completions: 3, Active: 1, Succeeded: 1, StartedAt: diagEpoch, CronJob: "nightly"}},
		{Namespace: "default", Name: "seed", Kind: service.WorkloadJob, Containers: containers, Job: &service.JobState{Result: service.JobFailed, Reason: "BackoffLimitExceeded", Message: "Job has reached the specified backoff limit", Completions: 1, Failed: 6, Suspend: true, StartedAt: diagEpoch, FinishedAt: ended.Time}},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("workloads (-want +got):\n%s", diff)
	}

	d, err := svc.DescribeWorkload(context.Background(), id, service.WorkloadJob, "default", "nightly-28942")
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(want[1], d.Workload); diff != "" {
		t.Errorf("job diagnosis (-want +got):\n%s", diff)
	}
	if len(d.Pods) != 1 || d.Pods[0].Name != "nightly-28942-a" {
		t.Errorf("job pods = %+v; want nightly-28942-a", d.Pods)
	}

	svc, cs, id = newFakeService(t, running, deployment("default", "api"))
	forbid(cs, "list", "jobs", false)
	if got, err = svc.ListWorkloads(context.Background(), id); err != nil || len(got) != 1 {
		t.Errorf("forbidden jobs: workloads = %+v, %v; want the Deployment", got, err)
	}
}
