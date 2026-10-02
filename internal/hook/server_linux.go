//go:build linux

package hook

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"time"

	"golang.org/x/sys/unix"

	"github.com/Amrzxk/nephos/internal/model"
	"github.com/Amrzxk/nephos/internal/network/netns"
	"github.com/Amrzxk/nephos/internal/store"
)

// ENIPlumber applies one ENI to a pinned instance namespace.
type ENIPlumber interface {
	EnsureENI(context.Context, model.VPC, model.Subnet, model.ENI, *netns.InstanceTarget) error
}

// NewHandler serves only the private, identity-checked plumbing operation.
func NewHandler(s *store.Store, network ENIPlumber) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /plumb", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 9*time.Second)
		defer cancel()
		var request Request
		r.Body = http.MaxBytesReader(w, r.Body, MaxStateBytes)
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&request); err != nil {
			http.Error(w, "invalid hook request", 400)
			return
		}
		var extra any
		if err := decoder.Decode(&extra); err != io.EOF {
			http.Error(w, "trailing hook request", 400)
			return
		}
		if err := request.validate(); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		var instance model.Instance
		var subnet model.Subnet
		var vpc model.VPC
		err := s.WithTx(ctx, func(tx *store.Tx) error {
			var err error
			instance, err = tx.GetInstance(ctx, "default", request.InstanceID)
			if err != nil {
				return err
			}
			if instance.DeletionRequested || instance.RuntimeID != request.ContainerID {
				return fmt.Errorf("runtime identity does not match live desired instance")
			}
			subnet, err = tx.GetSubnet(ctx, "default", instance.SubnetID)
			if err != nil {
				return err
			}
			vpc, err = tx.GetVPC(ctx, "default", subnet.VPCID)
			if err != nil {
				return err
			}
			if subnet.DeletionRequested || vpc.DeletionRequested || subnet.State != model.StateAvailable || vpc.State != model.StateAvailable ||
				subnet.ObservedGeneration != subnet.Generation || vpc.ObservedGeneration != vpc.Generation {
				return fmt.Errorf("network prerequisites are not converged")
			}
			return nil
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		target, err := netns.OpenInstance(ctx, request.PID, request.ContainerID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusForbidden)
			return
		}
		defer func() { _ = target.Close() }()
		if err := network.EnsureENI(ctx, vpc, subnet, instance.ENI, target); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		current, err := s.GetInstance(ctx, "default", instance.ID)
		if err != nil || current.Generation != instance.Generation || current.DeletionRequested || current.RuntimeID != request.ContainerID {
			http.Error(w, "instance desired state changed during plumbing", http.StatusConflict)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	return mux
}
func allowedPeer(uid uint32) bool { return uid == 0 }

type rootListener struct{ *net.UnixListener }

func (l *rootListener) Accept() (net.Conn, error) {
	for {
		conn, err := l.AcceptUnix()
		if err != nil {
			return nil, err
		}
		raw, err := conn.SyscallConn()
		var cred *unix.Ucred
		var peerErr error
		if err == nil {
			err = raw.Control(func(fd uintptr) { cred, peerErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED) })
		}
		if err == nil && peerErr == nil && cred != nil && allowedPeer(cred.Uid) {
			return conn, nil
		}
		_ = conn.Close()
	}
}

// Listen binds a fresh private socket and enforces root peer credentials.
func Listen(ctx context.Context, path string) (net.Listener, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Never unlink a live, stale or foreign file. The appliance initializes
	// /run/nephos as fresh private tmpfs on each start.
	if _, err := os.Lstat(path); err == nil {
		return nil, fmt.Errorf("hook socket path already exists")
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, fmt.Errorf("listen private hook socket: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = ln.Close()
		return nil, err
	}
	return &rootListener{ln}, nil
}
