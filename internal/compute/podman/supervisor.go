package podman

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/Amrzxk/nephos/internal/compute"
)

// A pin is observational authority for one exact session supervisor, never
// permission to signal a process selected by a name/prefix or reused PID.
type execSupervisor interface {
	keyID() string
	parentID() string
	gone() (bool, error) // requires disappearance/reaping, not only termination
	terminate(context.Context) error
	close() error
}
type supervisorScan func(context.Context, compute.Reference, string) ([]execSupervisor, error)

const containerMetadataRoot = "/var/lib/nephos/containers/overlay-containers"

func execSupervisorCandidate(args []string, ref compute.Reference, id string) bool {
	container, session := false, false
	for i := 1; i+1 < len(args); i++ {
		if args[i] == "--exit-command-arg" || args[i] == "--runtime-arg" {
			i++
			continue
		}
		container = container || args[i] == "-c" && args[i+1] == string(ref.ID)
		session = session || args[i] == "-u" && args[i+1] == id
	}
	return container && session
}

func matchesExecSupervisor(args []string, ref compute.Reference, id string) bool {
	if len(args) == 0 || args[0] != "/usr/bin/conmon" {
		return false
	}
	flags := make(map[string]string)
	exec := false
	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "--exit-command-arg", "--runtime-arg":
			i++ // Values are not conmon options, even if they resemble a flag.
		case "-e":
			if exec {
				return false
			}
			exec = true
		case "-c", "-u", "-b", "-n":
			flag := args[i]
			if _, duplicate := flags[flag]; duplicate || i+1 >= len(args) {
				return false
			}
			i++
			flags[flag] = args[i]
		}
	}
	bundle := filepath.Join(containerMetadataRoot, string(ref.ID), "userdata", id)
	return exec && flags["-c"] == string(ref.ID) && flags["-u"] == id && flags["-n"] == ref.Identity.InstanceID && flags["-b"] == bundle
}

// Called only after authoritative session removal. Podman's default delayed
// exit command preserves Wait status but leaves a conmon child sleeping. Kill
// only that proven child; its parent naturally reaps and exits. A parent that
// never forks/reaps, changed executable, or failed inspection is NOT success.
func joinExecSupervisors(ctx context.Context, ref compute.Reference, id string, scan supervisorScan, initial ...execSupervisor) (result error) {
	pins := make(map[string]execSupervisor)
	for _, pin := range initial {
		pins[pin.keyID()] = pin
	}
	defer func() {
		for _, pin := range pins {
			result = errors.Join(result, pin.close())
		}
	}()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		observed, err := scan(ctx, ref, id)
		if err != nil {
			return fmt.Errorf("inspect exec supervisors: %w", err)
		}
		for _, pin := range observed {
			if pins[pin.keyID()] != nil {
				if err := pin.close(); err != nil {
					return err
				}
				continue
			}
			pins[pin.keyID()] = pin
		}
		live := make(map[string]execSupervisor)
		for key, pin := range pins {
			gone, err := pin.gone()
			if err != nil {
				return fmt.Errorf("inspect pinned supervisor %s: %w", key, err)
			}
			if !gone {
				live[key] = pin
			}
		}
		if len(live) == 0 {
			return nil
		}
		for _, pin := range live {
			if live[pin.parentID()] == nil {
				continue
			}
			if err := pin.terminate(ctx); err != nil {
				return fmt.Errorf("close exec supervisor %s: %w", pin.keyID(), err)
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("join exec supervisors: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}
