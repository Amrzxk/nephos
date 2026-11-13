//go:build linux

package podman

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/Amrzxk/nephos/internal/compute"
)

type procSupervisor struct {
	pid, fd       int
	birth, parent string
	ref           compute.Reference
	execID        string
}

const procRoot = "/proc"

type supervisorProbe struct {
	open     func(int) (int, error)
	close    func(int) error
	exited   func(int) (bool, error)
	stat     func(int) (string, string, int, error)
	identity func(int, compute.Reference, string) (bool, error)
}

func (p *procSupervisor) keyID() string    { return strconv.Itoa(p.pid) + ":" + p.birth }
func (p *procSupervisor) parentID() string { return p.parent }
func (p *procSupervisor) close() error     { return unix.Close(p.fd) }

func procStat(pid int) (state, birth string, parent int, err error) {
	raw, err := os.ReadFile(filepath.Join(procRoot, strconv.Itoa(pid), "stat"))
	if err != nil {
		return "", "", 0, err
	}
	end := strings.LastIndex(string(raw), ") ")
	if end < 0 {
		return "", "", 0, fmt.Errorf("malformed process stat")
	}
	fields := strings.Fields(string(raw[end+2:]))
	if len(fields) < 20 {
		return "", "", 0, fmt.Errorf("short process stat")
	}
	parent, err = strconv.Atoi(fields[1])
	if err != nil {
		return "", "", 0, err
	}
	return fields[0], fields[19], parent, nil
}

func pidfdExited(fd int) (bool, error) {
	fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
	_, err := unix.Poll(fds, 0)
	if err != nil {
		return false, err
	}
	if fds[0].Revents&unix.POLLNVAL != 0 {
		return false, fmt.Errorf("invalid supervisor pidfd")
	}
	return fds[0].Revents&(unix.POLLIN|unix.POLLHUP) != 0, nil
}

func (p *procSupervisor) gone() (bool, error) {
	exited, err := pidfdExited(p.fd)
	if err != nil {
		return false, err
	}
	_, birth, _, err := procStat(p.pid)
	if os.IsNotExist(err) {
		return exited, nil
	}
	if err != nil {
		return false, err
	}
	// A different birth proves the old process was reaped, not merely killed.
	if birth != p.birth {
		return exited, nil
	}
	return false, nil // Includes a terminated but still unreaped zombie.
}

func procExecIdentity(pid int, ref compute.Reference, id string) (bool, error) {
	base := filepath.Join(procRoot, strconv.Itoa(pid))
	raw, err := os.ReadFile(filepath.Join(base, "cmdline"))
	if err != nil {
		return false, err
	}
	if !matchesExecSupervisor(strings.Split(strings.TrimRight(string(raw), "\x00"), "\x00"), ref, id) {
		return false, nil
	}
	trusted, err := os.Stat("/usr/bin/conmon")
	if err != nil {
		return false, err
	}
	executable, err := os.Stat(filepath.Join(base, "exe"))
	if err != nil {
		return false, err
	}
	if !os.SameFile(trusted, executable) {
		return false, fmt.Errorf("supervisor executable is not appliance conmon")
	}
	// This signaling authority is appliance-only. No host/guest process can
	// acquire it by spoofing argv; require the trusted appliance PID1 and all
	// three namespace identities, not merely the supervisor's comm/basename.
	initArgs, err := os.ReadFile("/proc/1/cmdline")
	if err != nil {
		return false, err
	}
	args := strings.Split(string(initArgs), "\x00")
	if len(args) < 3 || args[0] != "/usr/bin/catatonit" || args[1] != "--" || args[2] != "/usr/local/bin/nephos-entrypoint" {
		return false, fmt.Errorf("supervisor closure is restricted to the private Nephos appliance")
	}
	for _, kind := range []string{"pid", "mnt", "user"} {
		self, err := os.Stat("/proc/self/ns/" + kind)
		if err != nil {
			return false, err
		}
		initNS, err := os.Stat("/proc/1/ns/" + kind)
		if err != nil {
			return false, err
		}
		candidate, err := os.Stat(filepath.Join(base, "ns", kind))
		if err != nil {
			return false, err
		}
		if !os.SameFile(self, initNS) || !os.SameFile(self, candidate) {
			return false, fmt.Errorf("supervisor %s namespace is not appliance-owned", kind)
		}
	}
	return true, nil
}

func (p *procSupervisor) terminate(ctx context.Context) (result error) {
	if gone, err := p.gone(); err != nil || gone {
		return err
	}
	exited, err := pidfdExited(p.fd)
	if err != nil || exited {
		return err
	}
	// Freeze the pinned process before revalidation: a pidfd prevents PID
	// reuse, but without STOP an execv could change the signaling target after
	// its executable was inspected. Always resume on refusal/error.
	if err := unix.PidfdSendSignal(p.fd, unix.SIGSTOP, nil, 0); err != nil {
		if errors.Is(err, unix.ESRCH) {
			return nil
		}
		return err
	}
	defer func() {
		err := unix.PidfdSendSignal(p.fd, unix.SIGCONT, nil, 0)
		if err != nil && !errors.Is(err, unix.ESRCH) {
			result = errors.Join(result, err)
		}
	}()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		state, birth, _, err := procStat(p.pid)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if birth != p.birth {
			return fmt.Errorf("pinned supervisor PID changed")
		}
		if state == "Z" {
			return nil
		}
		if state == "T" || state == "t" {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
	match, err := procExecIdentity(p.pid, p.ref, p.execID)
	if err != nil {
		return err
	}
	if !match {
		return fmt.Errorf("supervisor executable/identity changed; refusing termination")
	}
	return unix.PidfdSendSignal(p.fd, unix.SIGKILL, nil, 0)
}

func scanExecSupervisors(ctx context.Context, ref compute.Reference, id string) (pins []execSupervisor, result error) {
	defer func() {
		if result != nil {
			for _, pin := range pins {
				result = errors.Join(result, pin.close())
			}
			pins = nil
		}
	}()
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return pins, err
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		comm, err := os.ReadFile(filepath.Join(procRoot, entry.Name(), "comm"))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return pins, err
		}
		if strings.TrimSpace(string(comm)) != "conmon" {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(procRoot, entry.Name(), "cmdline"))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return pins, err
		}
		args := strings.Split(strings.TrimRight(string(raw), "\x00"), "\x00")
		if !execSupervisorCandidate(args, ref, id) {
			continue
		}
		if !matchesExecSupervisor(args, ref, id) {
			return pins, fmt.Errorf("ambiguous marked exec supervisor %d", pid)
		}
		pin, err := pinExecSupervisor(pid, ref, id)
		if err != nil {
			return pins, err
		}
		if pin != nil {
			pins = append(pins, pin)
		}
	}
	return pins, nil
}

func pinExecSupervisor(pid int, ref compute.Reference, id string) (_ execSupervisor, result error) {
	return pinExecSupervisorUsing(pid, ref, id, supervisorProbe{
		open:  func(pid int) (int, error) { return unix.PidfdOpen(pid, 0) },
		close: unix.Close, exited: pidfdExited, stat: procStat, identity: procExecIdentity,
	})
}

func pinExecSupervisorUsing(pid int, ref compute.Reference, id string, probe supervisorProbe) (_ execSupervisor, result error) {
	fd, err := probe.open(pid)
	if errors.Is(err, unix.ESRCH) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	keep := false
	defer func() {
		if !keep {
			result = errors.Join(result, probe.close(fd))
		}
	}()
	exited, err := probe.exited(fd)
	if err != nil {
		return nil, err
	}
	_, birth, parent, err := probe.stat(pid)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if exited {
		return nil, fmt.Errorf("candidate exited before identity binding but is not reaped")
	}
	match, err := probe.identity(pid, ref, id)
	if os.IsNotExist(err) {
		// exe/ns disappearance can mean an unreaped zombie. Only disappearance
		// of stat proves reaping; otherwise identity cannot be safely bound.
		if _, _, _, statErr := probe.stat(pid); os.IsNotExist(statErr) {
			return nil, nil
		}
		return nil, fmt.Errorf("candidate identity vanished before reaping: %w", err)
	}
	if err != nil {
		return nil, err
	}
	if !match {
		return nil, fmt.Errorf("supervisor changed identity while pinning")
	}
	_, parentBirth, _, err := probe.stat(parent)
	if err != nil {
		return nil, fmt.Errorf("inspect supervisor parent: %w", err)
	}
	_, err = probe.exited(fd)
	if err != nil {
		return nil, err
	}
	// An exit AFTER identity binding is retained. gone() will wait for the
	// parent/reaper to remove the original stat entry, even with empty argv.
	keep = true
	return &procSupervisor{pid: pid, fd: fd, birth: birth, parent: strconv.Itoa(parent) + ":" + parentBirth, ref: ref, execID: id}, nil
}
