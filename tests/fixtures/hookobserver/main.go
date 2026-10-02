//go:build linux

// Command hookobserver is a smoke-only wrapper around the packaged OCI hook.
// It is copied into a test-owned appliance, never shipped in either image.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/vishvananda/netlink"

	"github.com/Amrzxk/nephos/internal/hook"
	"github.com/Amrzxk/nephos/internal/network/netns"
)

const proofDir = "/run/nephos/hook-proofs"

func observe(ctx context.Context) error {
	raw, err := io.ReadAll(io.LimitReader(os.Stdin, 65537))
	if err != nil {
		return fmt.Errorf("read OCI state: %w", err)
	}
	request, err := hook.DecodeState(bytes.NewReader(raw))
	if err != nil {
		return err
	}
	if _, err := os.Stat("/run/nephos/force-hook-failure"); err == nil {
		if err := os.MkdirAll(proofDir, 0o700); err != nil {
			return err
		}
		proof, err := json.Marshal(struct {
			InstanceID    string `json:"instance_id"`
			ForcedFailure bool   `json:"forced_failure"`
		}{request.InstanceID, true})
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(proofDir, request.InstanceID+".failed"), proof, 0o600); err != nil {
			return err
		}
		return fmt.Errorf("forced smoke createRuntime failure")
	} else if !os.IsNotExist(err) {
		return err
	}
	cmd := exec.CommandContext(ctx, "/usr/local/bin/nephos-hook.production")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = bytes.NewReader(raw), os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("production hook: %w", err)
	}
	target, err := netns.OpenInstance(ctx, request.PID, request.ContainerID)
	if err != nil {
		return err
	}
	defer func() { _ = target.Close() }()
	var addresses []string
	if err := netns.WithInstanceHandle(ctx, target, func(handle *netlink.Handle) error {
		link, err := handle.LinkByName("eth0")
		if err != nil {
			return err
		}
		if link.Type() != "veth" {
			return fmt.Errorf("eth0 is not a veth before PID 1")
		}
		items, err := handle.AddrList(link, netlink.FAMILY_V4)
		if err != nil {
			return err
		}
		for _, item := range items {
			addresses = append(addresses, item.IPNet.String())
		}
		if len(addresses) != 1 {
			return fmt.Errorf("eth0 IPv4 addresses before PID 1: %v", addresses)
		}
		return nil
	}); err != nil {
		return err
	}
	proof, err := json.Marshal(struct {
		InstanceID string   `json:"instance_id"`
		RuntimeID  string   `json:"runtime_id"`
		Addresses  []string `json:"addresses"`
	}{request.InstanceID, request.ContainerID, addresses})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(proofDir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(proofDir, request.InstanceID), proof, 0o600)
}

func main() {
	//nolint:forbidigo // The test-only executable creates its root context in main.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	err := observe(ctx)
	cancel()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
