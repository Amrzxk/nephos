package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/coder/websocket"
	"golang.org/x/sys/unix"
	"golang.org/x/term"

	"github.com/Amrzxk/nephos/internal/tunnel/console"
)

func testConsolePTY(t *testing.T) (master, slave *os.File) {
	t.Helper()
	fd, err := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	master = os.NewFile(uintptr(fd), "ptmx")
	t.Cleanup(func() { _ = master.Close() })
	if err := unix.IoctlSetPointerInt(fd, unix.TIOCSPTLCK, 0); err != nil {
		t.Fatal(err)
	}
	number, err := unix.IoctlGetInt(fd, unix.TIOCGPTN)
	if err != nil {
		t.Fatal(err)
	}
	fd, err = unix.Open(fmt.Sprintf("/dev/pts/%d", number), unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	slave = os.NewFile(uintptr(fd), "pts")
	t.Cleanup(func() { _ = slave.Close() })
	if err := unix.IoctlSetWinsize(fd, unix.TIOCSWINSZ, &unix.Winsize{Row: 24, Col: 80}); err != nil {
		t.Fatal(err)
	}
	return master, slave
}

func TestConsoleInteractiveRealTTY(t *testing.T) {
	for _, outcome := range []string{"exit", "error", "interrupt"} {
		t.Run(outcome, func(t *testing.T) {
			master, slave := testConsolePTY(t)
			before, err := term.GetState(int(slave.Fd()))
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			done := make(chan error, 1)
			cfg := consoleFixture(t, func(ctx context.Context, conn *websocket.Conn) {
				_, raw, err := conn.Read(ctx)
				if err != nil {
					done <- err
					return
				}
				start, err := console.ParseClientText(raw)
				if err != nil || start.Start == nil || !start.Start.TTY || start.Start.Rows != 24 || start.Start.Cols != 80 {
					done <- fmt.Errorf("TTY start %s %w", raw, err)
					return
				}
				if _, err := master.Write([]byte{'s', 0, 'h'}); err != nil {
					done <- err
					return
				}
				kind, raw, err := conn.Read(ctx)
				if err != nil || kind != websocket.MessageBinary || !bytes.Equal(raw, []byte{0, 's', 0, 'h'}) {
					done <- fmt.Errorf("TTY input %q %w", raw, err)
					return
				}
				if err := unix.IoctlSetWinsize(int(slave.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Row: 40, Col: 120}); err != nil {
					done <- err
					return
				}
				_, raw, err = conn.Read(ctx)
				if err != nil {
					done <- err
					return
				}
				resize, err := console.ParseClientText(raw)
				if err != nil || resize.Resize == nil || resize.Resize.Rows != 40 || resize.Resize.Cols != 120 {
					done <- fmt.Errorf("TTY resize %s %w", raw, err)
					return
				}
				if outcome == "interrupt" {
					cancel()
					_, _, err = conn.Read(ctx)
					if err == nil {
						done <- fmt.Errorf("interrupt did not disconnect")
						return
					}
					done <- nil
					return
				}
				_ = conn.Write(ctx, websocket.MessageBinary, []byte{1, 'o', 0, 'k'})
				raw = []byte(`{"type":"exit","exit_code":0}`)
				if outcome == "error" {
					raw = []byte(`{"type":"error","message":"failed"}`)
				}
				_ = conn.Write(ctx, websocket.MessageText, raw)
				_ = conn.Close(websocket.StatusNormalClosure, "")
				done <- nil
			})
			dir := t.TempDir()
			stdout, err := os.Create(filepath.Join(dir, "out"))
			if err != nil {
				t.Fatal(err)
			}
			defer stdout.Close()
			stderr, err := os.Create(filepath.Join(dir, "err"))
			if err != nil {
				t.Fatal(err)
			}
			defer stderr.Close()
			code := runConsole(ctx, []string{"i-00000000000000001"}, slave, stdout, stderr, cfg, nil)
			if (outcome == "exit" && code != 0) || (outcome != "exit" && code == 0) {
				t.Fatalf("%s status %d", outcome, code)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			after, err := term.GetState(int(slave.Fd()))
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, after) {
				t.Fatal("real terminal attributes not restored")
			}
			if outcome == "exit" {
				out, err := os.ReadFile(stdout.Name())
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(out, []byte{'o', 0, 'k'}) {
					t.Fatalf("TTY stdout %q", out)
				}
			}
		})
	}
}
