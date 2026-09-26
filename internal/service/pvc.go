package service

import (
	"cmp"
	"context"
	"slices"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// KubePVC is a PersistentVolumeClaim in scope. Size is the bound volume's capacity, or what the claim asks for while it has none.
type KubePVC struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	// Phase is Pending, Bound or Lost.
	Phase        string   `json:"phase"`
	Size         string   `json:"size"`
	StorageClass string   `json:"storageClass,omitempty"`
	Volume       string   `json:"volume,omitempty"`
	AccessModes  []string `json:"accessModes,omitempty"`
}

// PVCDiagnosis is a claim with the pods that mount it.
type PVCDiagnosis struct {
	PVC KubePVC `json:"pvc"`
	// Pods is nil and PodsError set when the pods could not be read; so are Events and EventsError.
	Pods        []KubePod   `json:"pods"`
	PodsError   string      `json:"podsError,omitempty"`
	Events      []KubeEvent `json:"events"`
	EventsError string      `json:"eventsError,omitempty"`
}

// ListPVCs lists PersistentVolumeClaims in scope.
func (s *Service) ListPVCs(ctx context.Context, clusterID string) ([]KubePVC, error) {
	k, err := s.clusterClient(clusterID)
	if err != nil {
		return nil, err
	}
	claims, err := cached(ctx, k, "pvcs", []string{"pvcs"}, k.scope(), &corev1.PersistentVolumeClaim{}, func(ns string) listWatcher[*corev1.PersistentVolumeClaimList] {
		return k.client.CoreV1().PersistentVolumeClaims(ns)
	})
	if err != nil {
		return nil, err
	}
	out := make([]KubePVC, 0, len(claims))
	for _, c := range claims {
		out = append(out, kubePVC(c))
	}
	slices.SortFunc(out, func(a, b KubePVC) int {
		return cmp.Or(cmp.Compare(a.Namespace, b.Namespace), cmp.Compare(a.Name, b.Name))
	})
	return out, nil
}

// DescribePVC is one claim and the pods of its namespace that mount it, which is what a Pending claim holds up.
func (s *Service) DescribePVC(ctx context.Context, clusterID, namespace, name string) (PVCDiagnosis, error) {
	k, err := s.clusterClient(clusterID)
	if err != nil {
		return PVCDiagnosis{}, err
	}
	c, err := k.client.CoreV1().PersistentVolumeClaims(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return PVCDiagnosis{}, wrapForbidden(err)
	}
	d := PVCDiagnosis{PVC: kubePVC(c)}
	pods, err := scopedPods(ctx, k)
	if err != nil {
		d.PodsError = errorMessage(err)
	}
	for _, p := range pods {
		if p.Namespace == namespace && slices.ContainsFunc(p.Spec.Volumes, func(v corev1.Volume) bool { return volumeClaim(p, v) == name }) {
			d.Pods = append(d.Pods, kubePod(p))
		}
	}
	slices.SortFunc(d.Pods, func(a, b KubePod) int { return cmp.Compare(a.Name, b.Name) })
	d.Events, err = objectEvents(ctx, k, "PersistentVolumeClaim", namespace, name, string(c.UID))
	if err != nil {
		d.EventsError = errorMessage(err)
	}
	return d, nil
}

// volumeClaim is the claim a pod volume mounts: the one it names, or the one an ephemeral volume gets, named after pod and volume.
func volumeClaim(p *corev1.Pod, v corev1.Volume) string {
	switch {
	case v.PersistentVolumeClaim != nil:
		return v.PersistentVolumeClaim.ClaimName
	case v.Ephemeral != nil:
		return p.Name + "-" + v.Name
	}
	return ""
}

func kubePVC(c *corev1.PersistentVolumeClaim) KubePVC {
	o := KubePVC{Namespace: c.Namespace, Name: c.Name, Phase: string(c.Status.Phase), Volume: c.Spec.VolumeName}
	if c.Spec.StorageClassName != nil {
		o.StorageClass = *c.Spec.StorageClassName
	}
	size, ok := c.Status.Capacity[corev1.ResourceStorage]
	if !ok {
		size = c.Spec.Resources.Requests[corev1.ResourceStorage]
	}
	o.Size = size.String()
	for _, m := range c.Spec.AccessModes {
		o.AccessModes = append(o.AccessModes, string(m))
	}
	return o
}
