// Package server spawns and supervises a single llama-server child, and
// stores/reads the session.json record that lets a separate llamaman
// invocation reattach to it.
package server

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

// Process wraps a llama-server child. There are two flavors:
//
//   - Owner (from Spawn): we hold a *exec.Cmd and call Wait() on it. Done
//     closes when Wait returns; Stop signals the process directly.
//   - Adopted (from Adopt): we only know the PID. Done closes when a
//     polling goroutine sees the PID disappear; Stop signals via
//     syscall.Kill.
//
// Owner mode is what `llamaman <alias>` produces. Adopted mode is what a
// reattaching llamaman gets when session.json points at a live PID.
type Process struct {
	Pid     int
	LogPath string
	Started time.Time
	Argv    []string

	cmd     *exec.Cmd // nil when adopted
	log     *os.File  // nil when adopted
	done    chan struct{}
	waitErr error
}

// Spawn starts argv[0] with argv[1:], redirecting stdout+stderr to logPath
// (truncated). The child runs in its own session so it survives this
// process's exit if the user detaches.
func Spawn(argv []string, logPath string) (*Process, error) {
	if len(argv) < 1 {
		return nil, fmt.Errorf("server.Spawn: argv must include the binary path")
	}
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return nil, fmt.Errorf("server.Spawn: ensure log dir: %w", err)
	}
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return nil, fmt.Errorf("server.Spawn: open log: %w", err)
	}

	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdout = f
	cmd.Stderr = f
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	if err := cmd.Start(); err != nil {
		f.Close()
		return nil, fmt.Errorf("server.Spawn: start %s: %w", argv[0], err)
	}

	p := &Process{
		Pid:     cmd.Process.Pid,
		LogPath: logPath,
		Started: time.Now(),
		Argv:    argv,
		cmd:     cmd,
		log:     f,
		done:    make(chan struct{}),
	}
	go func() {
		p.waitErr = cmd.Wait()
		_ = f.Close()
		close(p.done)
	}()
	return p, nil
}

// Adopt builds a Process for an already-running PID recorded in
// session.json. Done closes when the PID disappears; Stop signals via
// syscall.Kill.
func Adopt(s Session) *Process {
	p := &Process{
		Pid:     s.PID,
		LogPath: s.LogPath,
		Started: s.StartedAt,
		Argv:    s.Command,
		done:    make(chan struct{}),
	}
	go p.pollPidExit()
	return p
}

func (p *Process) pollPidExit() {
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	for range tick.C {
		if !IsLive(p.Pid) {
			close(p.done)
			return
		}
	}
}

// IsOwner reports whether we are the parent of this child (true after
// Spawn) or merely tracking it (false after Adopt). Adopted mode disables
// kill-on-quit's log-file removal, since the log file is shared with the
// owner.
func (p *Process) IsOwner() bool { return p.cmd != nil }

// Done is closed when the child has exited.
func (p *Process) Done() <-chan struct{} { return p.done }

// WaitErr is the error from cmd.Wait() (owner only). Always nil for
// adopted processes.
func (p *Process) WaitErr() error { return p.waitErr }

// Stop sends SIGTERM, waits up to grace for clean shutdown, then SIGKILLs
// the process. Returns when the child has exited (or when polling sees
// the PID disappear, in adopted mode).
func (p *Process) Stop(grace time.Duration) {
	select {
	case <-p.done:
		return
	default:
	}
	if p.cmd != nil {
		_ = p.cmd.Process.Signal(syscall.SIGTERM)
	} else {
		_ = syscall.Kill(p.Pid, syscall.SIGTERM)
	}
	select {
	case <-p.done:
		return
	case <-time.After(grace):
		if p.cmd != nil {
			_ = p.cmd.Process.Kill()
		} else {
			_ = syscall.Kill(p.Pid, syscall.SIGKILL)
		}
		<-p.done
	}
}

// RemoveLog deletes the log file. Safe to call after the process has
// exited. Used by run mode when the user explicitly kills the server
// (DESIGN.md §5.4).
func (p *Process) RemoveLog() error {
	if p.LogPath == "" {
		return nil
	}
	err := os.Remove(p.LogPath)
	if err != nil && os.IsNotExist(err) {
		return nil
	}
	return err
}
