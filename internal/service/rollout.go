package service

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type WorkloadKind string

const (
	WorkloadDeployment  WorkloadKind = "deployment"
	WorkloadStatefulSet WorkloadKind = "statefulset"
	WorkloadDaemonSet   WorkloadKind = "daemonset"
	WorkloadCronJob     WorkloadKind = "cronjob"
)

type RolloutState string

const (
	RolloutProgressing RolloutState = "progressing"
	RolloutStuck       RolloutState = "stuck"
	RolloutComplete    RolloutState = "complete"
)

// KubeWorkload is a Deployment, StatefulSet, DaemonSet or CronJob in scope; Rollout is set for the first three, CronJob for the last.
type KubeWorkload struct {
	Namespace  string              `json:"namespace"`
	Name       string              `json:"name"`
	Kind       WorkloadKind        `json:"kind"`
	Containers []WorkloadContainer `json:"containers"`
	Rollout    *Rollout            `json:"rollout,omitempty"`
	CronJob    *CronJobState       `json:"cronJob,omitempty"`
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

type WorkloadDiagnosis struct {
	Workload KubeWorkload `json:"workload"`
	// Events is nil and EventsError set when the events could not be listed; the rest of the diagnosis still stands.
	Events      []KubeEvent `json:"events"`
	EventsError string      `json:"eventsError,omitempty"`
}

// ListWorkloads lists Deployments, StatefulSets, DaemonSets and CronJobs in scope with their rollout state.
func (s *Service) ListWorkloads(ctx context.Context, clusterID string) ([]KubeWorkload, error) {
	k, err := s.clusterClient(clusterID)
	if err != nil {
		return nil, err
	}
	var out []KubeWorkload
	for _, ns := range k.scope() {
		apps := k.client.AppsV1()
		deployments, err := apps.Deployments(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, wrapForbidden(err)
		}
		for i := range deployments.Items {
			out = append(out, deploymentWorkload(&deployments.Items[i]))
		}
		statefulSets, err := apps.StatefulSets(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, wrapForbidden(err)
		}
		for i := range statefulSets.Items {
			out = append(out, statefulSetWorkload(&statefulSets.Items[i]))
		}
		daemonSets, err := apps.DaemonSets(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, wrapForbidden(err)
		}
		for i := range daemonSets.Items {
			out = append(out, daemonSetWorkload(&daemonSets.Items[i]))
		}
		// A role that reads apps but not batch still gets its Deployments; the CronJobs are simply absent.
		cronJobs, err := k.client.BatchV1().CronJobs(ns).List(ctx, metav1.ListOptions{})
		if err != nil && !apierrors.IsForbidden(err) {
			return nil, err
		}
		if err != nil {
			continue
		}
		for i := range cronJobs.Items {
			out = append(out, cronJobWorkload(&cronJobs.Items[i]))
		}
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
	switch kind {
	case WorkloadDeployment:
		obj, err := k.client.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return WorkloadDiagnosis{}, wrapForbidden(err)
		}
		d.Workload, meta, eventKind = deploymentWorkload(obj), obj, "Deployment"
	case WorkloadStatefulSet:
		obj, err := k.client.AppsV1().StatefulSets(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return WorkloadDiagnosis{}, wrapForbidden(err)
		}
		d.Workload, meta, eventKind = statefulSetWorkload(obj), obj, "StatefulSet"
	case WorkloadDaemonSet:
		obj, err := k.client.AppsV1().DaemonSets(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return WorkloadDiagnosis{}, wrapForbidden(err)
		}
		d.Workload, meta, eventKind = daemonSetWorkload(obj), obj, "DaemonSet"
	case WorkloadCronJob:
		obj, err := k.client.BatchV1().CronJobs(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return WorkloadDiagnosis{}, wrapForbidden(err)
		}
		d.Workload, meta, eventKind = cronJobWorkload(obj), obj, "CronJob"
	default:
		return WorkloadDiagnosis{}, fmt.Errorf("unknown workload kind %q", kind)
	}
	d.Events, err = objectEvents(ctx, k, eventKind, namespace, name, string(meta.GetUID()))
	if err != nil {
		d.EventsError = err.Error()
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
	return KubeWorkload{Namespace: d.Namespace, Name: d.Name, Kind: WorkloadDeployment, Containers: workloadContainers(d.Spec.Template.Spec), Rollout: r}
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
	return KubeWorkload{Namespace: ss.Namespace, Name: ss.Name, Kind: WorkloadStatefulSet, Containers: workloadContainers(ss.Spec.Template.Spec), Rollout: r}
}

func daemonSetWorkload(ds *appsv1.DaemonSet) KubeWorkload {
	st := ds.Status
	r := &Rollout{Desired: st.DesiredNumberScheduled, Updated: st.UpdatedNumberScheduled, Ready: st.NumberReady, Available: st.NumberAvailable, Revision: ds.Annotations["deprecated.daemonset.template.generation"], State: RolloutComplete}
	if ds.Spec.UpdateStrategy.Type != appsv1.OnDeleteDaemonSetStrategyType && (ds.Generation > st.ObservedGeneration || r.Updated < r.Desired || r.Available < r.Desired) {
		r.State = RolloutProgressing
	}
	return KubeWorkload{Namespace: ds.Namespace, Name: ds.Name, Kind: WorkloadDaemonSet, Containers: workloadContainers(ds.Spec.Template.Spec), Rollout: r}
}

func cronJobWorkload(cj *batchv1.CronJob) KubeWorkload {
	c := &CronJobState{Schedule: cj.Spec.Schedule, Suspend: cj.Spec.Suspend != nil && *cj.Spec.Suspend}
	if cj.Status.LastScheduleTime != nil {
		c.LastScheduled = cj.Status.LastScheduleTime.Time
	}
	return KubeWorkload{Namespace: cj.Namespace, Name: cj.Name, Kind: WorkloadCronJob, Containers: workloadContainers(cj.Spec.JobTemplate.Spec.Template.Spec), CronJob: c}
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
