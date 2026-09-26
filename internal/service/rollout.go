package service

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type WorkloadKind string

const (
	WorkloadDeployment  WorkloadKind = "deployment"
	WorkloadStatefulSet WorkloadKind = "statefulset"
	WorkloadDaemonSet   WorkloadKind = "daemonset"
	WorkloadCronJob     WorkloadKind = "cronjob"
	WorkloadJob         WorkloadKind = "job"
)

type RolloutState string

const (
	RolloutProgressing RolloutState = "progressing"
	RolloutStuck       RolloutState = "stuck"
	RolloutComplete    RolloutState = "complete"
)

// KubeWorkload is a Deployment, StatefulSet, DaemonSet, CronJob or Job in scope; Rollout is set for the first three,
// CronJob and Job for their kind.
type KubeWorkload struct {
	Namespace  string              `json:"namespace"`
	Name       string              `json:"name"`
	Kind       WorkloadKind        `json:"kind"`
	Containers []WorkloadContainer `json:"containers"`
	Rollout    *Rollout            `json:"rollout,omitempty"`
	CronJob    *CronJobState       `json:"cronJob,omitempty"`
	Job        *JobState           `json:"job,omitempty"`
	// Created is when the object was created, for its age.
	Created time.Time `json:"created"`
}

// WorkloadContainer is one container of the pod template, init containers first; Tag is the version a row shows, read from Image.
type WorkloadContainer struct {
	Name  string `json:"name"`
	Init  bool   `json:"init,omitempty"`
	Image string `json:"image"`
	Tag   string `json:"tag"`
}

// Rollout is where a workload's pods stand against its spec, read the way kubectl rollout status does.
type Rollout struct {
	Desired   int32 `json:"desired"`
	Updated   int32 `json:"updated"`
	Ready     int32 `json:"ready"`
	Available int32 `json:"available"`
	// Revision is the Deployment's revision number, a StatefulSet's update revision or a DaemonSet's template generation.
	Revision string       `json:"revision"`
	State    RolloutState `json:"state"`
	// Message is the Progressing condition's message; only Deployments have one.
	Message string `json:"message,omitempty"`
}

type CronJobState struct {
	Schedule string `json:"schedule"`
	Suspend  bool   `json:"suspend"`
	// LastScheduled is when the controller last created a Job, zero when it never has.
	LastScheduled time.Time `json:"lastScheduled"`
}

// JobResult is where a Job stands, read from its Complete and Failed conditions as kubectl does.
type JobResult string

const (
	JobRunning  JobResult = "running"
	JobComplete JobResult = "complete"
	JobFailed   JobResult = "failed"
)

// JobState is one run: its pods by outcome against the completions it needs, and when it started and ended.
type JobState struct {
	Result JobResult `json:"result"`
	// Reason and Message are the Failed condition's, such as BackoffLimitExceeded.
	Reason      string `json:"reason,omitempty"`
	Message     string `json:"message,omitempty"`
	Completions int32  `json:"completions"`
	Active      int32  `json:"active"`
	Succeeded   int32  `json:"succeeded"`
	Failed      int32  `json:"failed"`
	Suspend     bool   `json:"suspend"`
	// StartedAt is zero until the controller starts the Job, FinishedAt while it runs.
	StartedAt  time.Time `json:"startedAt"`
	FinishedAt time.Time `json:"finishedAt"`
	// CronJob is the CronJob that started it, empty for a Job created by hand.
	CronJob string `json:"cronJob,omitempty"`
}

type WorkloadDiagnosis struct {
	Workload KubeWorkload `json:"workload"`
	// Pods are the pods its selector matches; a CronJob's pods belong to its Jobs and are not listed.
	Pods []KubePod `json:"pods"`
	// Events is nil and EventsError set when the events could not be listed; the rest of the diagnosis still stands.
	Events      []KubeEvent `json:"events"`
	EventsError string      `json:"eventsError,omitempty"`
}

// ListWorkloads lists Deployments, StatefulSets, DaemonSets, CronJobs and Jobs in scope with their rollout or run state.
func (s *Service) ListWorkloads(ctx context.Context, clusterID string) ([]KubeWorkload, error) {
	k, err := s.clusterClient(clusterID)
	if err != nil {
		return nil, err
	}
	kinds, scope := []string{"workloads"}, k.scope()
	apps := k.client.AppsV1()
	deployments, err := cached(ctx, k, "deployments", kinds, scope, &appsv1.Deployment{}, func(ns string) listWatcher[*appsv1.DeploymentList] { return apps.Deployments(ns) })
	if err != nil {
		return nil, err
	}
	statefulSets, err := cached(ctx, k, "statefulsets", kinds, scope, &appsv1.StatefulSet{}, func(ns string) listWatcher[*appsv1.StatefulSetList] { return apps.StatefulSets(ns) })
	if err != nil {
		return nil, err
	}
	daemonSets, err := cached(ctx, k, "daemonsets", kinds, scope, &appsv1.DaemonSet{}, func(ns string) listWatcher[*appsv1.DaemonSetList] { return apps.DaemonSets(ns) })
	if err != nil {
		return nil, err
	}
	// A role that reads apps but not batch still gets its Deployments; the CronJobs and Jobs of a namespace it may not read
	// are simply absent, so each namespace is watched on its own.
	var cronJobs []*batchv1.CronJob
	var jobs []*batchv1.Job
	for _, ns := range scope {
		got, err := cached(ctx, k, "cronjobs/"+ns, kinds, []string{ns}, &batchv1.CronJob{}, func(ns string) listWatcher[*batchv1.CronJobList] { return k.client.BatchV1().CronJobs(ns) })
		if err != nil && !errors.Is(err, ErrForbidden) {
			return nil, err
		}
		cronJobs = append(cronJobs, got...)
		runs, err := cached(ctx, k, "jobs/"+ns, kinds, []string{ns}, &batchv1.Job{}, func(ns string) listWatcher[*batchv1.JobList] { return k.client.BatchV1().Jobs(ns) })
		if err != nil && !errors.Is(err, ErrForbidden) {
			return nil, err
		}
		jobs = append(jobs, runs...)
	}
	var out []KubeWorkload
	for _, d := range deployments {
		out = append(out, deploymentWorkload(d))
	}
	for _, ss := range statefulSets {
		out = append(out, statefulSetWorkload(ss))
	}
	for _, ds := range daemonSets {
		out = append(out, daemonSetWorkload(ds))
	}
	for _, cj := range cronJobs {
		out = append(out, cronJobWorkload(cj))
	}
	for _, j := range jobs {
		out = append(out, jobWorkload(j))
	}
	slices.SortFunc(out, func(a, b KubeWorkload) int {
		return cmp.Or(cmp.Compare(a.Namespace, b.Namespace), cmp.Compare(a.Name, b.Name), cmp.Compare(a.Kind, b.Kind))
	})
	return out, nil
}

// DescribeWorkload explains one workload's rollout; events it cannot list are reported in EventsError rather than failing.
func (s *Service) DescribeWorkload(ctx context.Context, clusterID string, kind WorkloadKind, namespace, name string) (WorkloadDiagnosis, error) {
	k, err := s.clusterClient(clusterID)
	if err != nil {
		return WorkloadDiagnosis{}, err
	}
	var d WorkloadDiagnosis
	var meta metav1.Object
	var eventKind string
	var selector *metav1.LabelSelector
	switch kind {
	case WorkloadDeployment:
		obj, err := k.client.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return WorkloadDiagnosis{}, wrapForbidden(err)
		}
		d.Workload, meta, eventKind, selector = deploymentWorkload(obj), obj, "Deployment", obj.Spec.Selector
	case WorkloadStatefulSet:
		obj, err := k.client.AppsV1().StatefulSets(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return WorkloadDiagnosis{}, wrapForbidden(err)
		}
		d.Workload, meta, eventKind, selector = statefulSetWorkload(obj), obj, "StatefulSet", obj.Spec.Selector
	case WorkloadDaemonSet:
		obj, err := k.client.AppsV1().DaemonSets(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return WorkloadDiagnosis{}, wrapForbidden(err)
		}
		d.Workload, meta, eventKind, selector = daemonSetWorkload(obj), obj, "DaemonSet", obj.Spec.Selector
	case WorkloadCronJob:
		obj, err := k.client.BatchV1().CronJobs(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return WorkloadDiagnosis{}, wrapForbidden(err)
		}
		d.Workload, meta, eventKind = cronJobWorkload(obj), obj, "CronJob"
	case WorkloadJob:
		obj, err := k.client.BatchV1().Jobs(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return WorkloadDiagnosis{}, wrapForbidden(err)
		}
		d.Workload, meta, eventKind, selector = jobWorkload(obj), obj, "Job", obj.Spec.Selector
	default:
		return WorkloadDiagnosis{}, fmt.Errorf("unknown workload kind %q", kind)
	}
	if selector != nil {
		sel, err := metav1.LabelSelectorAsSelector(selector)
		if err != nil {
			return WorkloadDiagnosis{}, err
		}
		list, err := k.client.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: sel.String()})
		if err != nil {
			return WorkloadDiagnosis{}, wrapForbidden(err)
		}
		for _, pod := range list.Items {
			d.Pods = append(d.Pods, kubePod(&pod))
		}
	}
	d.Events, err = objectEvents(ctx, k, eventKind, namespace, name, string(meta.GetUID()))
	if err != nil {
		d.EventsError = errorMessage(err)
	}
	return d, nil
}

func deploymentWorkload(d *appsv1.Deployment) KubeWorkload {
	st := d.Status
	r := &Rollout{Desired: replicas(d.Spec.Replicas), Updated: st.UpdatedReplicas, Ready: st.ReadyReplicas, Available: st.AvailableReplicas, Revision: d.Annotations[revisionAnnotation]}
	stuck := false
	for _, c := range st.Conditions {
		if c.Type == appsv1.DeploymentProgressing {
			r.Message, stuck = c.Message, c.Reason == "ProgressDeadlineExceeded"
		}
	}
	// A rollout the controller has not observed yet is progressing even while the last one's timeout still stands.
	switch {
	case d.Generation > st.ObservedGeneration:
		r.State = RolloutProgressing
	case stuck:
		r.State = RolloutStuck
	case r.Updated < r.Desired, st.Replicas > r.Updated, r.Available < r.Updated:
		r.State = RolloutProgressing
	default:
		r.State = RolloutComplete
	}
	return KubeWorkload{Namespace: d.Namespace, Name: d.Name, Kind: WorkloadDeployment, Containers: workloadContainers(d.Spec.Template.Spec), Rollout: r, Created: d.CreationTimestamp.Time}
}

func statefulSetWorkload(ss *appsv1.StatefulSet) KubeWorkload {
	st := ss.Status
	r := &Rollout{Desired: replicas(ss.Spec.Replicas), Updated: st.UpdatedReplicas, Ready: st.ReadyReplicas, Available: st.AvailableReplicas, Revision: st.UpdateRevision, State: RolloutComplete}
	var partition int32
	if ru := ss.Spec.UpdateStrategy.RollingUpdate; ru != nil && ru.Partition != nil {
		partition = *ru.Partition
	}
	// With OnDelete nothing rolls until pods are deleted by hand, so there is no rollout to follow.
	switch {
	case ss.Spec.UpdateStrategy.Type == appsv1.OnDeleteStatefulSetStrategyType:
	case ss.Generation > st.ObservedGeneration, r.Ready < r.Desired:
		r.State = RolloutProgressing
	case partition > 0:
		if r.Updated < r.Desired-partition {
			r.State = RolloutProgressing
		}
	case st.UpdateRevision != st.CurrentRevision:
		r.State = RolloutProgressing
	}
	return KubeWorkload{Namespace: ss.Namespace, Name: ss.Name, Kind: WorkloadStatefulSet, Containers: workloadContainers(ss.Spec.Template.Spec), Rollout: r, Created: ss.CreationTimestamp.Time}
}

func daemonSetWorkload(ds *appsv1.DaemonSet) KubeWorkload {
	st := ds.Status
	r := &Rollout{Desired: st.DesiredNumberScheduled, Updated: st.UpdatedNumberScheduled, Ready: st.NumberReady, Available: st.NumberAvailable, Revision: ds.Annotations["deprecated.daemonset.template.generation"], State: RolloutComplete}
	if ds.Spec.UpdateStrategy.Type != appsv1.OnDeleteDaemonSetStrategyType && (ds.Generation > st.ObservedGeneration || r.Updated < r.Desired || r.Available < r.Desired) {
		r.State = RolloutProgressing
	}
	return KubeWorkload{Namespace: ds.Namespace, Name: ds.Name, Kind: WorkloadDaemonSet, Containers: workloadContainers(ds.Spec.Template.Spec), Rollout: r, Created: ds.CreationTimestamp.Time}
}

func cronJobWorkload(cj *batchv1.CronJob) KubeWorkload {
	c := &CronJobState{Schedule: cj.Spec.Schedule, Suspend: cj.Spec.Suspend != nil && *cj.Spec.Suspend}
	if cj.Status.LastScheduleTime != nil {
		c.LastScheduled = cj.Status.LastScheduleTime.Time
	}
	return KubeWorkload{Namespace: cj.Namespace, Name: cj.Name, Kind: WorkloadCronJob, Containers: workloadContainers(cj.Spec.JobTemplate.Spec.Template.Spec), CronJob: c, Created: cj.CreationTimestamp.Time}
}

func jobWorkload(j *batchv1.Job) KubeWorkload {
	st := j.Status
	s := &JobState{Result: JobRunning, Completions: replicas(j.Spec.Completions), Active: st.Active, Succeeded: st.Succeeded, Failed: st.Failed, Suspend: j.Spec.Suspend != nil && *j.Spec.Suspend}
	if st.StartTime != nil {
		s.StartedAt = st.StartTime.Time
	}
	for _, c := range st.Conditions {
		if c.Status != corev1.ConditionTrue {
			continue
		}
		switch c.Type {
		case batchv1.JobComplete:
			s.Result, s.FinishedAt = JobComplete, c.LastTransitionTime.Time
			if st.CompletionTime != nil {
				s.FinishedAt = st.CompletionTime.Time
			}
		case batchv1.JobFailed:
			s.Result, s.Reason, s.Message, s.FinishedAt = JobFailed, c.Reason, c.Message, c.LastTransitionTime.Time
		}
	}
	if ref := metav1.GetControllerOf(j); ref != nil && ref.Kind == "CronJob" {
		s.CronJob = ref.Name
	}
	return KubeWorkload{Namespace: j.Namespace, Name: j.Name, Kind: WorkloadJob, Containers: workloadContainers(j.Spec.Template.Spec), Job: s, Created: j.CreationTimestamp.Time}
}

func workloadContainers(spec corev1.PodSpec) []WorkloadContainer {
	var out []WorkloadContainer
	for _, c := range spec.InitContainers {
		out = append(out, WorkloadContainer{Name: c.Name, Init: true, Image: c.Image, Tag: imageTag(c.Image)})
	}
	for _, c := range spec.Containers {
		out = append(out, WorkloadContainer{Name: c.Name, Image: c.Image, Tag: imageTag(c.Image)})
	}
	return out
}

// imageTag is the digest's first 12 hex digits when the reference is pinned, otherwise the tag after the last colon
// that is not a registry port, and "latest" when there is none, as the kubelet reads it.
func imageTag(image string) string {
	if _, digest, ok := strings.Cut(image, "@"); ok {
		_, hex, _ := strings.Cut(digest, ":")
		return hex[:min(12, len(hex))]
	}
	if i := strings.LastIndex(image, ":"); i > strings.LastIndex(image, "/") {
		return image[i+1:]
	}
	return "latest"
}

// replicas is the spec's count, which defaults to one when unset.
func replicas(n *int32) int32 {
	if n == nil {
		return 1
	}
	return *n
}
