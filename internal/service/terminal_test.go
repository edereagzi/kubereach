//go:build !windows

package service_test

import (
	"errors"
	"regexp"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/edereagzi/kubereach/internal/service"
)

// terminalEvents routes Terminal events into channels, leaving other events to the previous Emit.
func terminalEvents(svc *service.Service) (<-chan service.ShellOutput, <-chan service.TerminalStatus) {
	output := make(chan service.ShellOutput, 1000)
	states := make(chan service.TerminalStatus, 100)
	emit := svc.Emit
	svc.Emit = func(name string, data any) {
		switch data := data.(type) {
		case service.TerminalOutput:
			// The same shape as a Shell's, so readOutput serves both.
			output <- service.ShellOutput(data)
		case service.TerminalStatus:
			states <- data
		default:
			emit(name, data)
		}
	}
	return output, states
}

func waitTerminalState(t *testing.T, states <-chan service.TerminalStatus, want service.State) service.TerminalStatus {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case st := <-states:
			if st.State == want {
				return st
			}
		case <-deadline:
			t.Fatalf("timed out waiting for terminal state %q", want)
		}
	}
}

func startTestTerminal(t *testing.T) (*service.Service, <-chan service.ShellOutput, <-chan service.TerminalStatus, service.TerminalStatus) {
	t.Helper()
	t.Setenv("SHELL", "/bin/sh")
	t.Setenv("PS1", "$ ")
	svc := service.New(t.TempDir()+"/config.json", nil)
	output, states := terminalEvents(svc)
	st, err := svc.StartTerminal("c1", 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = svc.StopTerminal(st.ID) })
	if st.ClusterID != "c1" || st.Shell != "/bin/sh" || st.State != service.StateConnected {
		t.Errorf("status = %+v, want c1, /bin/sh, connected", st)
	}
	return svc, output, states, st
}

// shellPID asks the shell for its process ID.
func shellPID(t *testing.T, svc *service.Service, output <-chan service.ShellOutput, id string) int {
	t.Helper()
	if err := svc.WriteTerminal(id, []byte("echo pid=$$.\n")); err != nil {
		t.Fatal(err)
	}
	// The typed line is echoed first, so output is read until the expanded one arrives.
	re := regexp.MustCompile(`pid=(\d+)\.`)
	var got string
	for !re.MatchString(got) {
		got += readOutput(t, output, id, ".")
	}
	pid, _ := strconv.Atoi(re.FindStringSubmatch(got)[1])
	return pid
}

func processGone(t *testing.T, pid int) {
	t.Helper()
	if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Errorf("shell %d still running (kill 0: %v)", pid, err)
	}
}

func TestTerminal_RunsTheUserShellInAPTY(t *testing.T) {
	svc, output, states, st := startTestTerminal(t)

	if err := svc.WriteTerminal(st.ID, []byte("echo hi-$((1+1))\n")); err != nil {
		t.Fatal(err)
	}
	readOutput(t, output, st.ID, "hi-2")
	// Readline reads the size and writes it back as it preps the TTY, so a resize landing in between is undone; the
	// shell is then resized again, as the next resize of the view would.
	sizes := regexp.MustCompile(`\d+ \d+\r\n`)
	for try := 0; ; try++ {
		if err := svc.ResizeTerminal(st.ID, 100, 30); err != nil {
			t.Fatal(err)
		}
		if err := svc.WriteTerminal(st.ID, []byte("stty size\n")); err != nil {
			t.Fatal(err)
		}
		var got string
		for !sizes.MatchString(got) {
			got += readOutput(t, output, st.ID, "\n")
		}
		if sizes.FindString(got) == "30 100\r\n" {
			break
		}
		if try == 2 {
			t.Fatalf("stty size = %q, want 30 100", sizes.FindString(got))
		}
	}
	if got := svc.TerminalStatuses(); len(got) != 1 || got[0].ID != st.ID {
		t.Errorf("statuses = %v, want the one Terminal", got)
	}

	if err := svc.WriteTerminal(st.ID, []byte("exit 3\n")); err != nil {
		t.Fatal(err)
	}
	waitTerminalState(t, states, service.StateStopped)
	if got := svc.TerminalStatuses(); len(got) != 0 {
		t.Errorf("statuses after exit = %v, want none", got)
	}
	if err := svc.WriteTerminal(st.ID, []byte("x")); err == nil {
		t.Error("write to an ended Terminal succeeded")
	}
}

func TestTerminal_AnnouncedBeforeItsFirstOutput(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	svc := service.New(t.TempDir()+"/config.json", nil)
	events := make(chan string, 100)
	svc.Emit = func(name string, _ any) { events <- name }
	st, err := svc.StartTerminal("c1", 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = svc.StopTerminal(st.ID) })
	if err := svc.WriteTerminal(st.ID, []byte("echo hi\n")); err != nil {
		t.Fatal(err)
	}
	if got := <-events; got != service.EventTerminalState {
		t.Errorf("first event = %q, want %q", got, service.EventTerminalState)
	}
}

func TestTerminal_KeepsItsLatestOutputForAReloadedWindow(t *testing.T) {
	svc, output, _, st := startTestTerminal(t)

	// More than the tail holds, then a marker that must be its end.
	if err := svc.WriteTerminal(st.ID, []byte("head -c 600000 /dev/zero | tr '\\0' x; echo; echo end-$((1+1))\n")); err != nil {
		t.Fatal(err)
	}
	readOutput(t, output, st.ID, "end-2")
	time.Sleep(50 * time.Millisecond)
	tail, err := svc.TerminalTail(st.ID)
	if err != nil {
		t.Fatal(err)
	}
	if data := tail.Data; len(data) > service.TailSize || !regexp.MustCompile(`x+\r\nend-2\r\n`).Match(data) {
		t.Errorf("tail is %d bytes ending %q, want at most %d ending with the marker", len(data), data[max(0, len(data)-40):], service.TailSize)
	}
	if err := svc.ResizeTerminal(st.ID, 100, 30); err != nil {
		t.Fatal(err)
	}
	if tail, _ := svc.TerminalTail(st.ID); tail.Cols != 100 || tail.Rows != 30 {
		t.Errorf("tail size = %dx%d, want the PTY's 100x30", tail.Cols, tail.Rows)
	}
	if _, err := svc.TerminalTail("gone"); err == nil {
		t.Error("tail of an unknown Terminal succeeded")
	}
}

func TestTerminal_StopEndsTheShell(t *testing.T) {
	svc, output, states, st := startTestTerminal(t)
	pid := shellPID(t, svc, output, st.ID)

	start := time.Now()
	if err := svc.StopTerminal(st.ID); err != nil {
		t.Fatal(err)
	}
	// Past a second it was killed rather than hung up.
	if d := time.Since(start); d > time.Second {
		t.Errorf("stop took %v, want the hangup to end the shell", d)
	}
	waitTerminalState(t, states, service.StateStopped)
	processGone(t, pid)
	if got := svc.TerminalStatuses(); len(got) != 0 {
		t.Errorf("statuses after stop = %v, want none", got)
	}
}

func TestTerminal_ShutdownEndsEveryShell(t *testing.T) {
	svc, output, _, first := startTestTerminal(t)
	second, err := svc.StartTerminal("c2", 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	pids := []int{shellPID(t, svc, output, first.ID), shellPID(t, svc, output, second.ID)}

	svc.Shutdown()
	for _, pid := range pids {
		processGone(t, pid)
	}
}

func TestTerminal_DeletingTheClusterEndsItsTerminals(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	svc, _ := newService(t)
	clusters, err := svc.ImportKubeconfigs([]string{writeKubeconfig(t)})
	if err != nil {
		t.Fatal(err)
	}
	gone, err := svc.StartTerminal(clusters[0].ID, 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	kept, err := svc.StartTerminal(clusters[1].ID, 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = svc.StopTerminal(kept.ID) })

	if err := svc.DeleteCluster(clusters[0].ID); err != nil {
		t.Fatal(err)
	}
	if got := svc.TerminalStatuses(); len(got) != 1 || got[0].ID != kept.ID {
		t.Errorf("statuses = %v, want only %s, not %s", got, kept.ID, gone.ID)
	}
}
