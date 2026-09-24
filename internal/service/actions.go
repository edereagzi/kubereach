package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

const revisionAnnotation = "deployment.kubernetes.io/revision"

// RestartWorkload rolls every pod of a Deployment, StatefulSet or DaemonSet the way kubectl rollout restart does.
func (s *Service) RestartWorkload(ctx context.Context, clusterID string, kind WorkloadKind, namespace, name string) error {
	k, err := s.clusterClient(clusterID)
	if err != nil {
		return err
	}
	patch := fmt.Appendf(nil, `{"spec":{"template":{"metadata":{"annotations":{"kubectl.kubernetes.io/restartedAt":%q}}}}}`, time.Now().Format(time.RFC3339))
	apps := k.client.AppsV1()
	switch kind {
	case WorkloadDeployment:
		_, err = apps.Deployments(namespace).Patch(ctx, name, types.StrategicMergePatchType, patch, metav1.PatchOptions{})
	case WorkloadStatefulSet:
		_, err = apps.StatefulSets(namespace).Patch(ctx, name, types.StrategicMergePatchType, patch, metav1.PatchOptions{})
	case WorkloadDaemonSet:
		_, err = apps.DaemonSets(namespace).Patch(ctx, name, types.StrategicMergePatchType, patch, metav1.PatchOptions{})
	default:
		return userErrorf("A %s cannot be restarted", kind)
	}
	return wrapForbidden(err)
}

// DeletePod deletes a pod with its default grace period, so its controller replaces it.
func (s *Service) DeletePod(ctx context.Context, clusterID, namespace, name string) error {
	k, err := s.clusterClient(clusterID)
	if err != nil {
		return err
	}
	return wrapForbidden(k.client.CoreV1().Pods(namespace).Delete(ctx, name, metav1.DeleteOptions{}))
}

// ScaleWorkload sets a Deployment's or StatefulSet's replicas through the scale subresource.
func (s *Service) ScaleWorkload(ctx context.Context, clusterID string, kind WorkloadKind, namespace, name string, replicas int32) error {
	if replicas < 0 {
		return userErrorf("Replicas cannot be negative")
	}
	k, err := s.clusterClient(clusterID)
	if err != nil {
		return err
	}
	patch := fmt.Appendf(nil, `{"spec":{"replicas":%d}}`, replicas)
	apps := k.client.AppsV1()
	switch kind {
	case WorkloadDeployment:
		_, err = apps.Deployments(namespace).Patch(ctx, name, types.MergePatchType, patch, metav1.PatchOptions{}, "scale")
	case WorkloadStatefulSet:
		_, err = apps.StatefulSets(namespace).Patch(ctx, name, types.MergePatchType, patch, metav1.PatchOptions{}, "scale")
	default:
		return userErrorf("A %s cannot be scaled", kind)
	}
	return wrapForbidden(err)
}

// RollbackDeployment puts back the pod template of the Deployment's previous revision, as kubectl rollout undo does.
func (s *Service) RollbackDeployment(ctx context.Context, clusterID, namespace, name string) error {
	k, err := s.clusterClient(clusterID)
	if err != nil {
		return err
	}
	deployments := k.client.AppsV1().Deployments(namespace)
	d, err := deployments.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return wrapForbidden(err)
	}
	if d.Spec.Paused {
		return userErrorf("Deployment %s is paused; resume it before rolling back", name)
	}
	selector, err := metav1.LabelSelectorAsSelector(d.Spec.Selector)
	if err != nil {
		return err
	}
	sets, err := k.client.AppsV1().ReplicaSets(namespace).List(ctx, metav1.ListOptions{LabelSelector: selector.String()})
	if err != nil {
		return wrapForbidden(err)
	}
	// The previous revision is the second highest among the Deployment's own ReplicaSets.
	var latest, previous *appsv1.ReplicaSet
	var latestRev, previousRev int64
	for i := range sets.Items {
		rs := &sets.Items[i]
		rev, err := strconv.ParseInt(rs.Annotations[revisionAnnotation], 10, 64)
		if err != nil || !metav1.IsControlledBy(rs, d) {
			continue
		}
		switch {
		case latest == nil || rev > latestRev:
			previous, previousRev = latest, latestRev
			latest, latestRev = rs, rev
		case previous == nil || rev > previousRev:
			previous, previousRev = rs, rev
		}
	}
	if previous == nil {
		return userErrorf("Deployment %s has no previous revision to roll back to", name)
	}
	template := previous.Spec.Template.DeepCopy()
	delete(template.Labels, appsv1.DefaultDeploymentUniqueLabelKey)
	// The resourceVersion read above makes the API server refuse the patch with a conflict if the Deployment changed since.
	patch, err := json.Marshal([]map[string]any{
		{"op": "add", "path": "/metadata/resourceVersion", "value": d.ResourceVersion},
		{"op": "replace", "path": "/spec/template", "value": template},
	})
	if err != nil {
		return err
	}
	_, err = deployments.Patch(ctx, name, types.JSONPatchType, patch, metav1.PatchOptions{})
	return wrapForbidden(err)
}
