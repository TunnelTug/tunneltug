//go:build linux

package main

// PID 1 owns every orphan in the VM (containerd shims reparent here), so one
// reaper collects all exit statuses and hands the ones we spawned to their waiters.
// Nothing in tug-init uses exec.Cmd.Wait: it would race the reaper.

import (
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

type reaper struct {
	mu      sync.Mutex
	waiters map[int]chan unix.WaitStatus
	early   map[int]unix.WaitStatus // exited before the spawner registered
}

func newReaper() *reaper {
	r := &reaper{waiters: map[int]chan unix.WaitStatus{}, early: map[int]unix.WaitStatus{}}
	sig := make(chan os.Signal, 64)
	signal.Notify(sig, syscall.SIGCHLD)
	go func() {
		for range sig {
			r.reapAll()
		}
	}()
	// SIGCHLD can coalesce; a slow sweep catches anything a burst hid.
	go func() {
		for range time.Tick(5 * time.Second) {
			r.reapAll()
		}
	}()
	return r
}

func (r *reaper) reapAll() {
	for {
		var ws unix.WaitStatus
		pid, err := unix.Wait4(-1, &ws, unix.WNOHANG, nil)
		if pid <= 0 || err != nil {
			return
		}
		r.mu.Lock()
		if ch, ok := r.waiters[pid]; ok {
			delete(r.waiters, pid)
			ch <- ws
		} else {
			r.early[pid] = ws
			if len(r.early) > 4096 { // orphans nobody waits for
				r.early = map[int]unix.WaitStatus{}
			}
		}
		r.mu.Unlock()
	}
}

// spawn fork-execs argv with the given stdio files and returns a channel that
// receives its exit status.
func (r *reaper) spawn(argv, env []string, dir string, files []uintptr) (int, <-chan unix.WaitStatus, error) {
	ch := make(chan unix.WaitStatus, 1)
	r.mu.Lock() // hold across fork so a fast exit cannot be reaped before we register
	defer r.mu.Unlock()
	pid, err := syscall.ForkExec(argv[0], argv, &syscall.ProcAttr{
		Dir:   dir,
		Env:   env,
		Files: files,
		Sys:   &syscall.SysProcAttr{Setsid: true},
	})
	if err != nil {
		return 0, nil, fmt.Errorf("exec %s: %w", argv[0], err)
	}
	if ws, ok := r.early[pid]; ok {
		delete(r.early, pid)
		ch <- ws
	} else {
		r.waiters[pid] = ch
	}
	return pid, ch, nil
}

func exitCode(ws unix.WaitStatus) int {
	if ws.Signaled() {
		return 128 + int(ws.Signal())
	}
	return ws.ExitStatus()
}

// service is a supervised long-running process (k3s, the network forwarder).
type service struct {
	name    string
	argv    []string
	env     []string
	logPath string

	mu       sync.Mutex
	pid      int
	state    string
	restarts int
	stopping bool
}

type supervisor struct {
	r        *reaper
	mu       sync.Mutex
	services []*service
}

func (s *supervisor) start(svc *service) {
	s.mu.Lock()
	s.services = append(s.services, svc)
	s.mu.Unlock()
	go s.loop(svc)
}

func (s *supervisor) loop(svc *service) {
	backoff := time.Second
	for {
		svc.mu.Lock()
		if svc.stopping {
			svc.state = "stopped"
			svc.mu.Unlock()
			return
		}
		svc.mu.Unlock()

		logf, err := os.OpenFile(svc.logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			logf, _ = os.OpenFile("/dev/console", os.O_WRONLY, 0)
		}
		devnull, _ := os.Open("/dev/null")
		started := time.Now()
		pid, done, err := s.r.spawn(svc.argv, svc.env, "/", []uintptr{devnull.Fd(), logf.Fd(), logf.Fd()})
		devnull.Close()
		if err != nil {
			logf.Close()
			bootLog("%s: %v", svc.name, err)
			svc.mu.Lock()
			svc.state = "failed: " + err.Error()
			svc.mu.Unlock()
			time.Sleep(backoff)
			continue
		}
		svc.mu.Lock()
		svc.pid, svc.state = pid, "running"
		svc.mu.Unlock()
		bootLog("%s: started pid %d", svc.name, pid)

		ws := <-done
		logf.Close()
		code := exitCode(ws)
		svc.mu.Lock()
		svc.pid = 0
		svc.state = fmt.Sprintf("exited(%d)", code)
		stopping := svc.stopping
		if !stopping {
			svc.restarts++
		}
		svc.mu.Unlock()
		if stopping {
			return
		}
		if time.Since(started) > time.Minute {
			backoff = time.Second
		} else if backoff < 30*time.Second {
			backoff *= 2
		}
		bootLog("%s: exited %d, restart in %s (tail %s)", svc.name, code, backoff, tailFile(svc.logPath, 3))
		time.Sleep(backoff)
	}
}

func (s *supervisor) states() map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]string{}
	for _, svc := range s.services {
		svc.mu.Lock()
		st := svc.state
		if svc.restarts > 0 {
			st += fmt.Sprintf(" restarts=%d", svc.restarts)
		}
		out[svc.name] = st
		svc.mu.Unlock()
	}
	return out
}

// stopAll sends SIGTERM to every service and waits up to grace for them to exit.
func (s *supervisor) stopAll(grace time.Duration) {
	s.mu.Lock()
	svcs := append([]*service(nil), s.services...)
	s.mu.Unlock()
	for _, svc := range svcs {
		svc.mu.Lock()
		svc.stopping = true
		if svc.pid > 0 {
			_ = unix.Kill(-svc.pid, unix.SIGTERM) // its session: k3s takes containerd with it
		}
		svc.mu.Unlock()
	}
	deadline := time.Now().Add(grace)
	for time.Now().Before(deadline) {
		alive := false
		for _, svc := range svcs {
			svc.mu.Lock()
			if svc.pid > 0 {
				alive = true
			}
			svc.mu.Unlock()
		}
		if !alive {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	for _, svc := range svcs {
		svc.mu.Lock()
		if svc.pid > 0 {
			_ = unix.Kill(-svc.pid, unix.SIGKILL)
		}
		svc.mu.Unlock()
	}
}

func tailFile(path string, n int) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return "-"
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, " | ")
}
