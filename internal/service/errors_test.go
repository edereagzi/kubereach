package service_test

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/url"
	"syscall"
	"testing"

	"github.com/google/go-cmp/cmp"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation/field"

	"github.com/edereagzi/kubereach/internal/service"
)

func TestDescribe(t *testing.T) {
	pods := schema.GroupResource{Resource: "pods"}
	apiURL := func(err error) error {
		return &url.Error{Op: "Get", URL: "https://10.0.0.1:6443/api/v1/pods", Err: err}
	}
	dial := func(err error) error {
		return &net.OpError{Op: "dial", Net: "tcp", Addr: &net.TCPAddr{IP: net.IPv4(10, 0, 0, 1), Port: 6443}, Err: err}
	}
	cases := []struct {
		name string
		err  error
		want service.ErrorInfo
	}{
		{"port in use", &service.PortInUseError{Port: 9099, Suggested: 51403},
			service.ErrorInfo{Code: "port-in-use", Message: "Local port 9099 is in use; 51403 is free", Port: 9099, Suggested: 51403}},
		{"passphrase", &service.CredentialError{Code: "passphrase", Target: "~/.ssh/id_ed25519"},
			service.ErrorInfo{Code: "passphrase", Message: "~/.ssh/id_ed25519 needs its passphrase", Target: "~/.ssh/id_ed25519"}},
		{"route down", fmt.Errorf("list pods: %w", service.ErrRouteDown),
			service.ErrorInfo{Message: "The Route is not connected"}},
		{"not found", apierrors.NewNotFound(pods, "api-0"),
			service.ErrorInfo{Message: "api-0 was not found in the Cluster"}},
		{"forbidden by role", apierrors.NewForbidden(pods, "", errors.New(`User "dev" cannot list resource "pods"`)),
			service.ErrorInfo{Code: "forbidden", Message: "Your role in this Cluster does not allow this"}},
		{"forbidden sentinel", fmt.Errorf("%w: %w", service.ErrForbidden, apierrors.NewForbidden(pods, "", errors.New("rbac"))),
			service.ErrorInfo{Code: "forbidden", Message: "Your role in this Cluster does not allow this"}},
		{"quota", apierrors.NewForbidden(pods, "api-0", errors.New("exceeded quota: compute, requested: pods=1, used: pods=10, limited: pods=10")),
			service.ErrorInfo{Message: "The Cluster refused the change: exceeded quota: compute, requested: pods=1, used: pods=10, limited: pods=10"}},
		{"webhook", &apierrors.StatusError{ErrStatus: metav1.Status{Status: metav1.StatusFailure, Code: 400, Reason: metav1.StatusReasonBadRequest,
			Message: `admission webhook "owners.example.com" denied the request: deployments must have an owner label`}},
			service.ErrorInfo{Message: "The Cluster refused the change: deployments must have an owner label"}},
		{"invalid", apierrors.NewInvalid(schema.GroupKind{Group: "apps", Kind: "Deployment"}, "api",
			field.ErrorList{field.Invalid(field.NewPath("spec", "replicas"), -1, "must be greater than or equal to 0")}),
			service.ErrorInfo{Message: "The Cluster refused the change: spec.replicas: Invalid value: -1: must be greater than or equal to 0"}},
		{"unauthorized", apierrors.NewUnauthorized("Unauthorized"),
			service.ErrorInfo{Message: "The Cluster did not accept the credentials in the kubeconfig"}},
		{"conflict", apierrors.NewConflict(schema.GroupResource{Group: "apps", Resource: "deployments"}, "api", errors.New("object has been modified")),
			service.ErrorInfo{Message: "It changed in the Cluster meanwhile; try again"}},
		{"busy", apierrors.NewTooManyRequests("slow down", 1),
			service.ErrorInfo{Message: "The Cluster could not answer right now; try again"}},
		{"refused", apiURL(dial(syscall.ECONNREFUSED)),
			service.ErrorInfo{Message: "10.0.0.1:6443 refused the connection"}},
		{"unreachable", apiURL(dial(syscall.EHOSTUNREACH)),
			service.ErrorInfo{Message: "10.0.0.1:6443 cannot be reached"}},
		{"dial timeout", apiURL(dial(&timeoutError{})),
			service.ErrorInfo{Message: "10.0.0.1:6443 did not answer in time"}},
		{"dns", apiURL(&net.DNSError{Name: "k8s.example.com", Err: "no such host", IsNotFound: true}),
			service.ErrorInfo{Message: "k8s.example.com could not be resolved"}},
		{"deadline", fmt.Errorf("dial pod: %w", context.DeadlineExceeded),
			service.ErrorInfo{Message: "The request timed out"}},
		{"untrusted certificate", apiURL(x509.UnknownAuthorityError{}),
			service.ErrorInfo{Message: "The Cluster's certificate could not be verified"}},
		{"missing file", &fs.PathError{Op: "open", Path: "/home/me/.kube/config", Err: fs.ErrNotExist},
			service.ErrorInfo{Message: "/home/me/.kube/config does not exist"}},
		{"unreadable file", &fs.PathError{Op: "open", Path: "/home/me/.kube/config", Err: fs.ErrPermission},
			service.ErrorInfo{Message: "Kubereach may not read /home/me/.kube/config"}},
		{"login command", apiURL(errors.New(`getting credentials: exec: executable gke-gcloud-auth-plugin not found`)),
			service.ErrorInfo{Message: "The login command in the kubeconfig failed"}},
		{"unknown", errors.New("yaml: line 3: mapping values are not allowed in this context"),
			service.ErrorInfo{Message: "Something went wrong"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if diff := cmp.Diff(c.want, service.Describe(c.err)); diff != "" {
				t.Errorf("Describe(%v) mismatch (-want +got):\n%s", c.err, diff)
			}
		})
	}
}

type timeoutError struct{}

func (timeoutError) Error() string   { return "i/o timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }
