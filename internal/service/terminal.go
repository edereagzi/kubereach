package service

import (
	"cmp"
	"os"
	"runtime"
	"slices"
	"sync"
	"syscall"
	"time"

	"github.com/aymanbagabas/go-pty"
)

// EventTerminalOutput carries a TerminalOutput per chunk read from a Terminal's PTY.
const EventTerminalOutput = "terminal:output"

type TerminalOutput struct {
	SessionID string `json:"sessionId"`
	Data      []byte `json:"data"`
}

// EventTerminalState carries a TerminalStatus when a Terminal starts, before any output, and when it ends; it is
// forgotten as it does.
const EventTerminalState = "terminal:state"

type TerminalStatus struct {
	ID string `json:"id"`
	// ClusterID is the Cluster whose bottom panel the Terminal is a tab of.
	ClusterID string `json:"clusterId"`
	Shell     string `json:"shell"`
	State     State  `json:"state"`
}

type terminalConn struct {
	pty      pty.Pty
	cmd      *pty.Cmd
	closePty func()
	done     chan struct{}
	status   TerminalStatus
}

// terminalHangup is how long a Terminal's shell has to exit after its PTY is closed before it is killed.
const terminalHangup = 3 * time.Second

// StartTerminal runs the user's shell in a PTY of cols by rows in their home directory; output arrives as
// EventTerminalOutput and its end as EventTerminalState.
func (s *Service) StartTerminal(clusterID string, cols, rows int) (TerminalStatus, error) {
	p, err := pty.New()
	if err != nil {
		return TerminalStatus{}, err
	}
	if err := p.Resize(cols, rows); err != nil {
		_ = p.Close()
		return TerminalStatus{}, err
	}
	name, args := userShell()
	cmd := p.Command(name, args...)
	cmd.Env = append(os.Environ(), "TERM=xterm-256color", "COLORTERM=truecolor")
	cmd.Dir, _ = os.UserHomeDir()
	if err := cmd.Start(); err != nil {
		_ = p.Close()
		return TerminalStatus{}, &userError{msg: "Could not start " + name, err: err}
	}
	tc := &terminalConn{
		pty:      p,
		cmd:      cmd,
		closePty: sync.OnceFunc(func() { _ = p.Close() }),
		done:     make(chan struct{}),
		status:   TerminalStatus{ID: newID(), ClusterID: clusterID, Shell: name, State: StateConnected},
	}
	s.mu.Lock()
	status := tc.status
	s.terminals[status.ID] = tc
	s.mu.Unlock()
	// A shell prompts at once, so the Terminal is announced before the reader starts or the UI would drop the prompt.
	s.Emit(EventTerminalState, status)
	go s.runTerminal(tc)
	return status, nil
}

// userShell is $SHELL as a login shell, so a Kubereach started from the desktop gets the PATH a terminal app would;
// on Windows it is PowerShell.
func userShell() (string, []string) {
	if runtime.GOOS == "windows" {
		return "powershell.exe", []string{"-NoLogo"}
	}
	return cmp.Or(os.Getenv("SHELL"), "/bin/sh"), []string{"-l"}
}

// runTerminal streams the PTY until the shell exits, then forgets the Terminal. The PTY is closed only after the
// exit: the parent's hold on it keeps reads open until then, on Unix and ConPTY alike.
// ponytail: a leftover process that still holds the TTY keeps the reader, and its last output, until it exits.
func (s *Service) runTerminal(tc *terminalConn) {
	defer close(tc.done)
	read := make(chan struct{})
	go func() {
		defer close(read)
		buf := make([]byte, 32*1024)
		for {
			n, err := tc.pty.Read(buf)
			if n > 0 {
				s.Emit(EventTerminalOutput, TerminalOutput{SessionID: tc.status.ID, Data: slices.Clone(buf[:n])})
			}
			if err != nil {
				return
			}
		}
	}()
	// However the shell exited, the Terminal simply ended.
	_ = tc.cmd.Wait()
	tc.closePty()
	select {
	case <-read:
	case <-time.After(terminalHangup):
	}
	s.mu.Lock()
	delete(s.terminals, tc.status.ID)
	tc.status.State = StateStopped
	status := tc.status
	s.mu.Unlock()
	s.Emit(EventTerminalState, status)
}

// WriteTerminal sends bytes to the Terminal's shell.
func (s *Service) WriteTerminal(id string, data []byte) error {
	tc, err := s.terminal(id)
	if err != nil {
		return err
	}
	_, err = tc.pty.Write(data)
	return err
}

// ResizeTerminal sets the Terminal's PTY to its view's new size.
func (s *Service) ResizeTerminal(id string, cols, rows int) error {
	tc, err := s.terminal(id)
	if err != nil {
		return err
	}
	return tc.pty.Resize(cols, rows)
}

// StopTerminal hangs up the Terminal's shell the way closing a terminal window does, killing it if it lingers, and
// returns once it has ended; one that already ended is a no-op. On Unix the hangup is SIGHUP, since a PTY read in
// flight holds its close back; Windows has no such signal, and closing the ConPTY ends the shell instead.
func (s *Service) StopTerminal(id string) error {
	s.mu.Lock()
	tc := s.terminals[id]
	s.mu.Unlock()
	if tc == nil {
		return nil
	}
	_ = tc.cmd.Process.Signal(syscall.SIGHUP)
	tc.closePty()
	select {
	case <-tc.done:
	case <-time.After(terminalHangup):
		_ = tc.cmd.Process.Kill()
		<-tc.done
	}
	return nil
}

// TerminalStatuses returns every running Terminal.
func (s *Service) TerminalStatuses() []TerminalStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]TerminalStatus, 0, len(s.terminals))
	for _, tc := range s.terminals {
		out = append(out, tc.status)
	}
	slices.SortFunc(out, func(a, b TerminalStatus) int { return cmp.Compare(a.ID, b.ID) })
	return out
}

func (s *Service) terminal(id string) (*terminalConn, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tc := s.terminals[id]
	if tc == nil {
		return nil, userErrorf("The Terminal has ended")
	}
	return tc, nil
}
