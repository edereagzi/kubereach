package service_test

import (
	"context"
	"testing"

	"github.com/edereagzi/kubereach/internal/service"
	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func claim(ns, name, class, size string, phase corev1.PersistentVolumeClaimPhase) *corev1.PersistentVolumeClaim {
	return &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		Spec: corev1.PersistentVolumeClaimSpec{
			StorageClassName: &class,
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources:        corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse(size)}},
		},
		Status: corev1.PersistentVolumeClaimStatus{Phase: phase},
	}
}

func mounting(p *corev1.Pod, claims ...string) *corev1.Pod {
	for _, c := range claims {
		p.Spec.Volumes = append(p.Spec.Volumes, corev1.Volume{Name: c, VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: c}}})
	}
	return p
}

// A bound claim shows the volume's capacity, which may exceed the request; a pending one what it asks for.
func TestPVCs(t *testing.T) {
	bound := claim("db", "data-postgres-0", "gp3", "20Gi", corev1.ClaimBound)
	bound.Spec.VolumeName = "pv-123"
	bound.Status.Capacity = corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("25Gi")}
	pending := claim("db", "uploads", "standard", "50Gi", corev1.ClaimPending)
	// An ephemeral volume's claim is named after its pod and volume.
	scratch := claim("db", "worker-scratch", "standard", "1Gi", corev1.ClaimBound)
	ephemeral := pod("db", "worker", "worker", corev1.PodRunning, true, diagEpoch)
	ephemeral.Spec.Volumes = []corev1.Volume{{Name: "scratch", VolumeSource: corev1.VolumeSource{Ephemeral: &corev1.EphemeralVolumeSource{}}}}
	ev := event("db", "uploads", "Warning", "ProvisioningFailed", "storageclass.storage.k8s.io \"standard\" not found", 3, diagEpoch)
	ev.InvolvedObject.Kind = "PersistentVolumeClaim"
	svc, cs, id := newFakeService(t, bound, pending, scratch, ev,
		mounting(pod("db", "uploads-sync", "sync", corev1.PodPending, false, diagEpoch), "uploads"),
		mounting(pod("db", "postgres-0", "postgres", corev1.PodRunning, true, diagEpoch), "data-postgres-0"),
		mounting(pod("other", "uploads-elsewhere", "x", corev1.PodRunning, true, diagEpoch), "uploads"),
		ephemeral,
	)
	ctx := context.Background()

	got, err := svc.ListPVCs(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	want := []service.KubePVC{
		{Namespace: "db", Name: "data-postgres-0", Phase: "Bound", Size: "25Gi", StorageClass: "gp3", Volume: "pv-123", AccessModes: []string{"ReadWriteOnce"}},
		{Namespace: "db", Name: "uploads", Phase: "Pending", Size: "50Gi", StorageClass: "standard", AccessModes: []string{"ReadWriteOnce"}},
		{Namespace: "db", Name: "worker-scratch", Phase: "Bound", Size: "1Gi", StorageClass: "standard", AccessModes: []string{"ReadWriteOnce"}},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("claims (-want +got):\n%s", diff)
	}

	d, err := svc.DescribePVC(ctx, id, "db", "uploads")
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(want[1], d.PVC); diff != "" {
		t.Errorf("claim (-want +got):\n%s", diff)
	}
	if len(d.Pods) != 1 || d.Pods[0].Name != "uploads-sync" {
		t.Errorf("mounting pods = %+v; want uploads-sync only", d.Pods)
	}
	if len(d.Events) != 1 || d.Events[0].Reason != "ProvisioningFailed" {
		t.Errorf("events = %+v; want the ProvisioningFailed one", d.Events)
	}

	if d, err = svc.DescribePVC(ctx, id, "db", "worker-scratch"); err != nil || len(d.Pods) != 1 || d.Pods[0].Name != "worker" {
		t.Errorf("ephemeral claim pods = %+v, %v; want worker", d.Pods, err)
	}

	forbid(cs, "list", "events", false)
	if d, err = svc.DescribePVC(ctx, id, "db", "uploads"); err != nil || d.Events != nil || d.EventsError == "" {
		t.Errorf("forbidden events = %+v, %v; want none with an error", d, err)
	}

	svc, cs, id = newFakeService(t, pending)
	forbid(cs, "list", "pods", false)
	if d, err = svc.DescribePVC(ctx, id, "db", "uploads"); err != nil || d.Pods != nil || d.PodsError == "" {
		t.Errorf("forbidden pods = %+v, %v; want the claim with a pods error", d, err)
	}
	if _, err := svc.DescribePVC(ctx, id, "db", "missing"); err == nil {
		t.Error("missing claim described without error")
	}
}
