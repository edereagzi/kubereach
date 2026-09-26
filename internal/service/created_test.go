package service_test

import (
	"context"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Every Overview list carries when its object was created, for the age column. Secrets are listed as metadata, which the
// fake clientset cannot serve; TestSecrets_ListedByNameOnlyReadOnGet covers theirs.
func TestLists_CarryCreated(t *testing.T) {
	created := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	meta := func(name string) metav1.ObjectMeta {
		return metav1.ObjectMeta{Namespace: "shop", Name: name, CreationTimestamp: metav1.NewTime(created)}
	}
	svc, _, id := newFakeService(t,
		&corev1.Service{ObjectMeta: meta("web")},
		&networkingv1.Ingress{ObjectMeta: meta("web")},
		&appsv1.Deployment{ObjectMeta: meta("web")},
		&appsv1.StatefulSet{ObjectMeta: meta("db")},
		&appsv1.DaemonSet{ObjectMeta: meta("agent")},
		&batchv1.CronJob{ObjectMeta: meta("report")},
		&batchv1.Job{ObjectMeta: meta("migrate")},
		&corev1.Pod{ObjectMeta: meta("web-1")},
		&corev1.ConfigMap{ObjectMeta: meta("settings")},
		&corev1.PersistentVolumeClaim{ObjectMeta: meta("data")},
		&autoscalingv2.HorizontalPodAutoscaler{ObjectMeta: meta("web")},
	)
	ctx := context.Background()
	check := func(list string, n int, err error, got func(i int) time.Time) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", list, err)
		}
		if n == 0 {
			t.Errorf("%s listed nothing", list)
		}
		for i := range n {
			if !got(i).Equal(created) {
				t.Errorf("%s[%d] created = %v; want %v", list, i, got(i), created)
			}
		}
	}

	services, err := svc.ListServices(ctx, id)
	check("services", len(services), err, func(i int) time.Time { return services[i].Created })
	ingresses, err := svc.ListIngresses(ctx, id)
	check("ingresses", len(ingresses), err, func(i int) time.Time { return ingresses[i].Created })
	workloads, err := svc.ListWorkloads(ctx, id)
	check("workloads", len(workloads), err, func(i int) time.Time { return workloads[i].Created })
	if len(workloads) != 5 {
		t.Errorf("workloads = %+v; want deployment, statefulset, daemonset, cronjob and job", workloads)
	}
	pods, err := svc.ListPods(ctx, id)
	check("pods", len(pods), err, func(i int) time.Time { return pods[i].Created })
	configMaps, err := svc.ListConfigMaps(ctx, id)
	check("configmaps", len(configMaps), err, func(i int) time.Time { return configMaps[i].Created })
	claims, err := svc.ListPVCs(ctx, id)
	check("pvcs", len(claims), err, func(i int) time.Time { return claims[i].Created })
	hpas, err := svc.ListHPAs(ctx, id)
	check("hpas", len(hpas), err, func(i int) time.Time { return hpas[i].Created })
}
