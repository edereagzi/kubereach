package service

import (
	"cmp"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
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
	// Namespace is the one kubectl in the Terminal defaults to, empty for its own default.
	Namespace string `json:"namespace"`
	State     State  `json:"state"`
}

type terminalConn struct {
	pty      pty.Pty
	cmd      *pty.Cmd
	closePty func()
	done     chan struct{}
	status   TerminalStatus
	proxy    *kubeProxy
	tail     outputTail
	// cols and rows are the PTY's size, guarded by the Service's mutex.
	cols, rows int
}

// terminalHangup is how long a Terminal's shell has to exit after its PTY is closed before it is killed.
const terminalHangup = 3 * time.Second

// StartTerminal runs the user's shell in a PTY of cols by rows in their home directory, with kubectl reaching the
// Cluster through Kubereach; output arrives as EventTerminalOutput and its end as EventTerminalState.
func (s *Service) StartTerminal(clusterID string, cols, rows int) (TerminalStatus, error) {
	cfg, err := s.LoadConfig()
	if err != nil {
		return TerminalStatus{}, err
	}
	i, err := findCluster(cfg, clusterID)
	if err != nil {
		return TerminalStatus{}, err
	}
	c := cfg.Clusters[i]
	namespace := s.terminalNamespace(c)
	proxy, err := s.startKubeProxy(clusterID)
	if err != nil {
		return TerminalStatus{}, &userError{msg: "Could not open the Terminal", err: err}
	}
	if err := proxy.writeKubeconfig(c.Name, namespace); err != nil {
		proxy.close()
		return TerminalStatus{}, &userError{msg: "Could not open the Terminal", err: err}
	}
	name, args, env, err := userShell(proxy.dir, proxy.kubeconfig)
	if err != nil {
		proxy.close()
		return TerminalStatus{}, &userError{msg: "Could not open the Terminal", err: err}
	}
	p, err := pty.New()
	if err != nil {
		proxy.close()
		return TerminalStatus{}, err
	}
	if err := p.Resize(cols, rows); err != nil {
		_ = p.Close()
		proxy.close()
		return TerminalStatus{}, err
	}
	cmd := p.Command(name, args...)
	cmd.Env = append(append(os.Environ(), "TERM=xterm-256color", "COLORTERM=truecolor", "KUBECONFIG="+proxy.kubeconfig, tokenEnv+"="+proxy.token), env...)
	cmd.Dir, _ = os.UserHomeDir()
	if err := cmd.Start(); err != nil {
		_ = p.Close()
		proxy.close()
		return TerminalStatus{}, &userError{msg: "Could not start " + name, err: err}
	}
	tc := &terminalConn{
		pty:      p,
		cmd:      cmd,
		closePty: sync.OnceFunc(func() { _ = p.Close() }),
		done:     make(chan struct{}),
		status:   TerminalStatus{ID: newID(), ClusterID: clusterID, Shell: name, Namespace: namespace, State: StateConnected},
		proxy:    proxy,
		cols:     cols,
		rows:     rows,
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
// on Windows it is PowerShell. A startup file may set KUBECONFIG too, so zsh, bash, fish and PowerShell set it back to
// kubeconfig after the user's own have run, zsh and bash from a startup script written to dir; env is added to the
// shell's environment.
func userShell(dir, kubeconfig string) (name string, args, env []string, err error) {
	if runtime.GOOS == "windows" {
		// -Command runs after the user's profile.
		return "powershell.exe", []string{"-NoLogo", "-NoExit", "-Command", "$env:KUBECONFIG = '" + strings.ReplaceAll(kubeconfig, "'", "''") + "'"}, nil, nil
	}
	name = cmp.Or(os.Getenv("SHELL"), "/bin/sh")
	quoted := "'" + strings.ReplaceAll(kubeconfig, "'", `'\''`) + "'"
	switch filepath.Base(name) {
	case "zsh":
		env = []string{"ZDOTDIR=" + dir, "KUBEREACH_ZDOTDIR=" + os.Getenv("ZDOTDIR")}
		return name, []string{"-l"}, env, os.WriteFile(filepath.Join(dir, ".zshenv"), []byte(fmt.Sprintf(zshenv, quoted)), 0o600)
	case "bash":
		// A login shell ignores --init-file, so the script starts it as one would be.
		script := filepath.Join(dir, "bashrc")
		return name, []string{"--init-file", script, "-i"}, nil, os.WriteFile(script, []byte(fmt.Sprintf(bashrc, quoted)), 0o600)
	case "fish":
		// -C runs after the user's config.
		return name, []string{"-l", "-C", "set -gx KUBECONFIG '" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(kubeconfig) + "'"}, nil, nil
	}
	return name, []string{"-l"}, nil, nil
}

// zshenv is read first as zsh starts, from the ZDOTDIR the Terminal gives it. It hands ZDOTDIR back to the user's, unset
// if they had none, and runs their .zshenv, so zsh goes on to read the rest of their startup files, following whatever
// ZDOTDIR a .zshenv sets, as it always would; KUBECONFIG is set back just before the first prompt, once all have run.
// ponytail: a startup file that assigns precmd_functions rather than adding to it drops the hook, and KUBECONFIG with it.
const zshenv = `if [[ -n $KUBEREACH_ZDOTDIR ]]; then ZDOTDIR=$KUBEREACH_ZDOTDIR; else unset ZDOTDIR; fi
unset KUBEREACH_ZDOTDIR
[[ -f "${ZDOTDIR:-$HOME}/.zshenv" ]] && source "${ZDOTDIR:-$HOME}/.zshenv"
_kubereach_kubeconfig() {
  export KUBECONFIG=%s
  precmd_functions=(${precmd_functions:#_kubereach_kubeconfig})
  unfunction _kubereach_kubeconfig
}
precmd_functions+=(_kubereach_kubeconfig)
`

// bashrc starts bash as a login shell would, then sets KUBECONFIG back.
const bashrc = `[ -r /etc/profile ] && . /etc/profile
if [ -r ~/.bash_profile ]; then . ~/.bash_profile
elif [ -r ~/.bash_login ]; then . ~/.bash_login
elif [ -r ~/.profile ]; then . ~/.profile
fi
export KUBECONFIG=%s
`

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
				tc.tail.add(buf[:n])
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
	tc.proxy.close()
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
	s.mu.Lock()
	tc.cols, tc.rows = cols, rows
	s.mu.Unlock()
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
	// The token is revoked at once, not once the shell has gone.
	tc.proxy.close()
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

// TerminalTail returns the Terminal's latest output, for a window that reloaded to draw again.
func (s *Service) TerminalTail(id string) (SessionTail, error) {
	tc, err := s.terminal(id)
	if err != nil {
		return SessionTail{}, err
	}
	s.mu.Lock()
	cols, rows := tc.cols, tc.rows
	s.mu.Unlock()
	return SessionTail{Data: tc.tail.bytes(), Cols: cols, Rows: rows}, nil
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
