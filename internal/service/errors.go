package service

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/url"
	"os"
	"strings"
	"syscall"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ErrorInfo is an error as the user sees it. Code and the fields beside it let the UI act on the error without reading
// the message.
type ErrorInfo struct {
	Code      string `json:"code,omitempty"`
	Message   string `json:"message"`
	Target    string `json:"target,omitempty"`
	Port      int    `json:"port,omitempty"`
	Suggested int    `json:"suggested,omitempty"`
}

// userError is an error Kubereach words for the user; err, when set, is kept for the log only.
type userError struct {
	msg string
	err error
}

func (e *userError) Error() string {
	if e.err != nil {
		return e.msg + ": " + e.err.Error()
	}
	return e.msg
}

func (e *userError) Unwrap() error { return e.err }

func userErrorf(format string, args ...any) error {
	return &userError{msg: fmt.Sprintf(format, args...)}
}

type sshServerError struct {
	addr string
	err  error
}

func (e *sshServerError) Error() string { return e.addr + ": " + e.err.Error() }
func (e *sshServerError) Unwrap() error { return e.err }

// An empty namespace is every namespace.
type namespaceError struct {
	namespace string
	err       error
}

func (e *namespaceError) Error() string { return e.err.Error() }
func (e *namespaceError) Unwrap() error { return e.err }

// Describe turns an error into what the user is shown. What Kubernetes, the network or SSH return is never shown as
// it is; an error Describe does not recognise is shown as a plain failure, and its text goes to stderr for development.
func Describe(err error) ErrorInfo {
	var credential *CredentialError
	var inUse *PortInUseError
	var exists *ForwardExistsError
	var server *sshServerError
	var user *userError
	switch {
	case errors.As(err, &credential):
		return ErrorInfo{Code: credential.Code, Target: credential.Target, Message: fmt.Sprintf("%s needs its %s", credential.Target, credential.Code)}
	case errors.As(err, &inUse):
		return ErrorInfo{Code: "port-in-use", Port: inUse.Port, Suggested: inUse.Suggested,
			Message: fmt.Sprintf("Local port %d is in use; %d is free", inUse.Port, inUse.Suggested)}
	case errors.As(err, &exists):
		pf := exists.Forward
		return ErrorInfo{Code: "forward-exists", Message: fmt.Sprintf("%s/%s:%d is already forwarded on localhost:%d",
			pf.Target.Namespace, pf.Target.Name, pf.RemotePort, pf.LocalPort)}
	case errors.As(err, &server):
		if msg := describeSSHServer(server); msg != "" {
			return ErrorInfo{Message: msg}
		}
		return Describe(server.err)
	case errors.As(err, &user):
		return ErrorInfo{Message: user.msg}
	}
	if msg, forbidden := describeAPI(err); msg != "" {
		if forbidden {
			return ErrorInfo{Code: "forbidden", Message: msg}
		}
		return ErrorInfo{Message: msg}
	}
	if msg := describeTransport(err); msg != "" {
		return ErrorInfo{Message: msg}
	}
	log.Printf("unexplained error: %v", err)
	return ErrorInfo{Message: "Something went wrong"}
}

func errorMessage(err error) string {
	if err == nil {
		return ""
	}
	return Describe(err).Message
}

func describeSSHServer(e *sshServerError) string {
	var keyErr *knownhosts.KeyError
	var channel *ssh.OpenChannelError
	switch {
	case isAuthFailure(e.err):
		return fmt.Sprintf("SSH server %s rejected the login", e.addr)
	case errors.As(e.err, &keyErr) && len(keyErr.Want) > 0:
		return fmt.Sprintf("The host key of SSH server %s does not match the one in known_hosts", e.addr)
	case errors.As(e.err, &channel):
		return fmt.Sprintf("SSH server %s cannot be reached from the SSH server before it", e.addr)
	case errors.Is(e.err, context.DeadlineExceeded) || errors.Is(e.err, os.ErrDeadlineExceeded):
		return fmt.Sprintf("SSH server %s did not answer in time", e.addr)
	}
	return ""
}

// describeAPI words an error the Kubernetes API returned. A change the Cluster's own policy refused keeps the policy's
// reason, which its administrators wrote for the user.
func describeAPI(err error) (msg string, forbidden bool) {
	forbiddenMsg := "Your role in this Cluster does not allow this"
	var ns *namespaceError
	if errors.As(err, &ns) && ns.namespace != "" {
		forbiddenMsg = "Your role may not read namespace " + ns.namespace
	}
	var status apierrors.APIStatus
	if !errors.As(err, &status) {
		if errors.Is(err, ErrForbidden) {
			return forbiddenMsg, true
		}
		return "", false
	}
	s := status.Status()
	if _, reason, ok := strings.Cut(s.Message, "denied the request: "); ok {
		return "The Cluster refused the change: " + reason, false
	}
	if i := strings.Index(s.Message, "exceeded quota: "); i >= 0 {
		return "The Cluster refused the change: " + s.Message[i:], false
	}
	switch s.Reason {
	case metav1.StatusReasonForbidden:
		return forbiddenMsg, true
	case metav1.StatusReasonInvalid:
		var causes []string
		if s.Details != nil {
			for _, c := range s.Details.Causes {
				causes = append(causes, c.Field+": "+c.Message)
			}
		}
		return "The Cluster refused the change: " + strings.Join(causes, "; "), false
	case metav1.StatusReasonNotFound:
		if s.Details != nil && s.Details.Name != "" {
			return s.Details.Name + " was not found in the Cluster", false
		}
		return "It was not found in the Cluster", false
	case metav1.StatusReasonAlreadyExists:
		return "It already exists in the Cluster", false
	case metav1.StatusReasonUnauthorized:
		return "The Cluster did not accept the credentials in the kubeconfig", false
	case metav1.StatusReasonConflict:
		return "It changed in the Cluster meanwhile; try again", false
	case metav1.StatusReasonTimeout, metav1.StatusReasonServerTimeout, metav1.StatusReasonTooManyRequests,
		metav1.StatusReasonServiceUnavailable, metav1.StatusReasonInternalError:
		return "The Cluster could not answer right now; try again", false
	}
	return "", false
}

func describeTransport(err error) string {
	var dns *net.DNSError
	var op *net.OpError
	var channel *ssh.OpenChannelError
	var path *fs.PathError
	var authority x509.UnknownAuthorityError
	var hostname x509.HostnameError
	var invalid x509.CertificateInvalidError
	var verification *tls.CertificateVerificationError
	switch {
	case errors.As(err, &dns):
		return dns.Name + " could not be resolved"
	case errors.As(err, &op) && op.Addr != nil:
		addr := op.Addr.String()
		switch {
		case op.Timeout():
			return addr + " did not answer in time"
		case errors.Is(op.Err, syscall.ECONNREFUSED):
			return addr + " refused the connection"
		default:
			return addr + " cannot be reached"
		}
	case errors.As(err, &channel):
		return "The Route's last SSH server cannot reach " + urlHost(err)
	case errors.As(err, &authority), errors.As(err, &hostname), errors.As(err, &invalid), errors.As(err, &verification):
		return "The Cluster's certificate could not be verified"
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, os.ErrDeadlineExceeded):
		return "The request timed out"
	case errors.As(err, &path) && errors.Is(err, fs.ErrNotExist):
		return path.Path + " does not exist"
	case errors.As(err, &path) && errors.Is(err, fs.ErrPermission):
		return "Kubereach may not read " + path.Path
	// client-go formats the plugin's failure with %v, so only its text is left to recognise.
	case strings.Contains(err.Error(), "getting credentials: "):
		log.Printf("kubeconfig login command: %v", err)
		return "The login command in the kubeconfig failed"
	}
	return ""
}

func urlHost(err error) string {
	var u *url.Error
	if errors.As(err, &u) {
		if parsed, perr := url.Parse(u.URL); perr == nil {
			return parsed.Host
		}
	}
	return "the Cluster's API"
}
