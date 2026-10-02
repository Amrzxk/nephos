package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/coder/websocket"
	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

// A stopped pipe consumer must not prevent interruption, worker reaping, or
// restoration of the actual local terminal. Both remote output channels matter:
// diagnostics also use stderr after the session returns.
func TestConsoleStalledOutputCancellation(t *testing.T) {
	for _, channel := range []byte{1, 2} {
		t.Run(fmt.Sprintf("channel-%d", channel), func(t *testing.T) {
			_, stdin := testConsolePTY(t)
			before, err := term.GetState(int(stdin.Fd()))
			if err != nil {
				t.Fatal(err)
			}
			pipeReader, pipeWriter, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer pipeReader.Close()
			defer pipeWriter.Close()
			// This intentionally models an inherited blocking stdout/stderr FD.
			fd := int(pipeWriter.Fd())
			// Two pipe pages leave room for remote stderr after the label.
			if _, err := unix.FcntlInt(uintptr(fd), unix.F_SETPIPE_SZ, 8192); err != nil {
				t.Fatal(err)
			}
			flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0)
			if err != nil {
				t.Fatal(err)
			}
			other, err := os.CreateTemp(t.TempDir(), "other-output")
			if err != nil {
				t.Fatal(err)
			}
			defer other.Close()
			stdout, stderr := pipeWriter, other
			if channel == 2 {
				stdout, stderr = other, pipeWriter
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			peerDone := make(chan struct{})
			cfg := consoleFixture(t, func(ctx context.Context, conn *websocket.Conn) {
				defer close(peerDone)
				if _, _, err := conn.Read(ctx); err != nil {
					return
				}
				payload := append([]byte{channel}, bytes.Repeat([]byte{'x'}, 65535)...)
				_ = conn.Write(ctx, websocket.MessageBinary, payload)
				for {
					if _, _, err := conn.Read(ctx); err != nil {
						return
					}
				}
			})
			// stderr is a separate stream only in command mode; the same real
			// stdin terminal is used there to check descriptor ownership. The
			// stdout case additionally proves raw terminal restoration.
			args := []string{"i-00000000000000001"}
			if channel == 2 {
				args = append(args, "--", "command")
			}
			done := make(chan int, 1)
			go func() { done <- runConsole(ctx, args, stdin, stdout, stderr, cfg, nil) }()
			deadline := time.Now().Add(3 * time.Second)
			for {
				queued, err := unix.IoctlGetInt(int(pipeReader.Fd()), unix.TIOCINQ)
				if err != nil {
					t.Fatal(err)
				}
				// The label may leave less than PIPE_BUF free on stderr, so
				// an atomic write can stall before every last byte is used.
				if queued >= 2048 {
					break
				}
				if time.Now().After(deadline) {
					cancel()
					_ = pipeReader.Close()
					t.Fatal("remote output did not fill the pipe")
				}
				time.Sleep(5 * time.Millisecond)
			}
			cancel()
			select {
			case code := <-done:
				if code == 0 {
					t.Error("canceled console reported success")
				}
			case <-time.After(time.Second):
				t.Error("canceled console remains blocked on local output")
				// Release the old implementation so a RED run does not leak
				// its goroutines or make httptest cleanup hang.
				_ = pipeReader.Close()
				select {
				case <-done:
				case <-time.After(2 * time.Second):
					t.Fatal("console did not return after releasing output")
				}
			}
			after, err := term.GetState(int(stdin.Fd()))
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Errorf("local terminal not restored: %v", err)
			}
			afterFlags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0)
			if err != nil || afterFlags != flags {
				t.Errorf("caller output descriptor closed or changed: flags=%d want=%d err=%v", afterFlags, flags, err)
			}
			select {
			case <-peerDone:
			case <-time.After(time.Second):
				t.Error("cancellation did not close remote session")
			}
		})
	}
}
