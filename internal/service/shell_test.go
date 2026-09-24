package service_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/edereagzi/kubereach/internal/service"
	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/remotecommand"
	"k8s.io/streaming/pkg/httpstream"
	"k8s.io/streaming/pkg/httpstream/spdy"
)

// execHandler speaks the exec subresource over SPDY: a command whose shell is not in a.shells fails on the error
// stream before any output; an available one prints "<shell>$ ", echoes stdin line by line and exits on "exit".
// Resize messages are recorded.
func (a *testAPI) execHandler(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	shell := q.Get("command")
	if q.Get("tty") != "true" || q.Get("stdin") != "true" || q.Get("stdout") != "true" {
		http.Error(w, "expected tty=true&stdin=true&stdout=true", http.StatusBadRequest)
		return
	}
	a.mu.Lock()
	a.execs = append(a.execs, q.Get("container")+":"+shell)
	available := a.shells[shell]
	a.mu.Unlock()
	if _, err := httpstream.Handshake(r, w, []string{"v4.channel.k8s.io"}); err != nil {
		return
	}
	streams := make(chan httpstream.Stream, 4)
	conn := spdy.NewResponseUpgrader().UpgradeResponse(w, r, func(s httpstream.Stream, _ <-chan struct{}) error {
		streams <- s
		return nil
	})
	if conn == nil {
		return
	}
	defer func() { _ = conn.Close() }()
	byType := map[string]httpstream.Stream{}
	for len(byType) < 4 {
		select {
		case s := <-streams:
			byType[s.Headers().Get(corev1.StreamType)] = s
		case <-conn.CloseChan():
			return
		}
	}
	errStream, stdin, stdout, resize := byType[corev1.StreamTypeError], byType[corev1.StreamTypeStdin], byType[corev1.StreamTypeStdout], byType[corev1.StreamTypeResize]
	if !available {
		_ = json.NewEncoder(errStream).Encode(metav1.Status{Status: metav1.StatusFailure, Message: fmt.Sprintf("exec: %q: executable file not found in $PATH", shell)})
		return
	}
	go func() {
		dec := json.NewDecoder(resize)
		for {
			var size remotecommand.TerminalSize
			if dec.Decode(&size) != nil {
				return
			}
			a.mu.Lock()
			a.resizes = append(a.resizes, size)
			a.mu.Unlock()
		}
	}()
	_, _ = fmt.Fprintf(stdout, "%s$ ", shell)
	sc := bufio.NewScanner(stdin)
	for sc.Scan() && sc.Text() != "exit" {
		if sc.Text() == "exit 1" {
			_ = json.NewEncoder(errStream).Encode(metav1.Status{
				Status:  metav1.StatusFailure,
				Reason:  "NonZeroExitCode",
				Details: &metav1.StatusDetails{Causes: []metav1.StatusCause{{Type: "ExitCode", Message: "1"}}},
			})
			return
		}
		_, _ = fmt.Fprintf(stdout, "%s\r\n", sc.Text())
	}
	_ = json.NewEncoder(errStream).Encode(metav1.Status{Status: metav1.StatusSuccess})
}

func (a *testAPI) execLog() ([]string, []remotecommand.TerminalSize) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.execs...), append([]remotecommand.TerminalSize(nil), a.resizes...)
}

// shellEvents routes shell events into channels, leaving other events to the previous Emit.
func shellEvents(svc *service.Service) (<-chan service.ShellOutput, <-chan service.ShellStatus) {
	output := make(chan service.ShellOutput, 100)
	states := make(chan service.ShellStatus, 100)
	emit := svc.Emit
	svc.Emit = func(name string, data any) {
		switch data := data.(type) {
		case service.ShellOutput:
			output <- data
		case service.ShellStatus:
			states <- data
		default:
			emit(name, data)
		}
	}
	return output, states
}

func waitShellState(t *testing.T, states <-chan service.ShellStatus, want service.State) service.ShellStatus {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case st := <-states:
			if st.State == want {
				return st
			}
			if st.State == service.StateError {
				t.Fatalf("shell failed while waiting for %q: %s", want, st.Error)
			}
		case <-deadline:
			t.Fatalf("timed out waiting for shell state %q", want)
		}
	}
}

// readOutput collects output for the session until it contains want.
func readOutput(t *testing.T, output <-chan service.ShellOutput, sessionID, want string) string {
	t.Helper()
	deadline := time.After(10 * time.Second)
	var got strings.Builder
	for !strings.Contains(got.String(), want) {
		select {
		case o := <-output:
			if o.SessionID == sessionID {
				got.Write(o.Data)
			}
		case <-deadline:
			t.Fatalf("timed out waiting for output %q, got %q", want, got.String())
		}
	}
	return got.String()
}

func TestShell_TriesShellsInOrderAndRoundTripsBytes(t *testing.T) {
	f := newForwardFixture(t, twoContainerPod("default", "api-0"))
	f.api.shells = map[string]bool{"sh": true}
	output, states := shellEvents(f.svc)
	ctx := context.Background()

	st, err := f.svc.StartShell(ctx, service.ShellTarget{ClusterID: f.cluster, Namespace: "default", Pod: "api-0", Container: "sidecar"}, 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	connected := waitShellState(t, states, service.StateConnected)
	if connected.Shell != "sh" {
		t.Errorf("shell = %q, want sh", connected.Shell)
	}
	readOutput(t, output, st.ID, "sh$ ")
	if err := f.svc.WriteShell(st.ID, []byte("echo hi\n")); err != nil {
		t.Fatal(err)
	}
	readOutput(t, output, st.ID, "echo hi\r\n")
	if err := f.svc.ResizeShell(st.ID, 120, 40); err != nil {
		t.Fatal(err)
	}
	// Resize is asynchronous; the echo after it proves the ordering.
	if err := f.svc.WriteShell(st.ID, []byte("after\n")); err != nil {
		t.Fatal(err)
	}
	readOutput(t, output, st.ID, "after\r\n")
	time.Sleep(50 * time.Millisecond)

	execs, resizes := f.api.execLog()
	if diff := cmp.Diff([]string{"sidecar:bash", "sidecar:zsh", "sidecar:sh"}, execs); diff != "" {
		t.Errorf("shells tried mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]remotecommand.TerminalSize{{Width: 80, Height: 24}, {Width: 120, Height: 40}}, resizes); diff != "" {
		t.Errorf("resizes mismatch (-want +got):\n%s", diff)
	}

	if err := f.svc.StopShell(st.ID); err != nil {
		t.Fatal(err)
	}
	waitShellState(t, states, service.StateStopped)
	if got := f.svc.ShellStatuses(); len(got) != 0 {
		t.Errorf("sessions after stop = %v, want none", got)
	}
}

func TestShell_DefaultsToFirstContainerAndEndsWhenShellExits(t *testing.T) {
	for _, exit := range []string{"exit", "exit 1"} {
		t.Run(exit, func(t *testing.T) {
			f := newForwardFixture(t, twoContainerPod("default", "api-0"))
			f.api.shells = map[string]bool{"bash": true}
			output, states := shellEvents(f.svc)

			st, err := f.svc.StartShell(context.Background(), service.ShellTarget{ClusterID: f.cluster, Namespace: "default", Pod: "api-0"}, 80, 24)
			if err != nil {
				t.Fatal(err)
			}
			if st.Target.Container != "app" {
				t.Errorf("container = %q, want app", st.Target.Container)
			}
			waitShellState(t, states, service.StateConnected)
			readOutput(t, output, st.ID, "bash$ ")
			if err := f.svc.WriteShell(st.ID, []byte(exit+"\n")); err != nil {
				t.Fatal(err)
			}
			waitShellState(t, states, service.StateStopped)
			if got := f.svc.ShellStatuses(); len(got) != 0 {
				t.Errorf("sessions after exit = %v, want none", got)
			}
			if err := f.svc.WriteShell(st.ID, []byte("x")); err == nil {
				t.Error("write to an ended session succeeded")
			}
		})
	}
}

func TestShell_NoShellAvailableFails(t *testing.T) {
	f := newForwardFixture(t, twoContainerPod("default", "api-0"))
	_, states := shellEvents(f.svc)

	_, err := f.svc.StartShell(context.Background(), service.ShellTarget{ClusterID: f.cluster, Namespace: "default", Pod: "api-0"}, 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.After(10 * time.Second)
	for {
		select {
		case st := <-states:
			if st.State == service.StateError {
				if st.Error != "The pod has none of bash, zsh, sh" {
					t.Errorf("error = %q, want the shells tried named", st.Error)
				}
				execs, _ := f.api.execLog()
				if diff := cmp.Diff([]string{"app:bash", "app:zsh", "app:sh"}, execs); diff != "" {
					t.Errorf("shells tried mismatch (-want +got):\n%s", diff)
				}
				return
			}
		case <-deadline:
			t.Fatal("timed out waiting for the session to fail")
		}
	}
}
