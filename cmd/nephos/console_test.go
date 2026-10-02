package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/Amrzxk/nephos/internal/tunnel/console"
)

func consoleFixture(t *testing.T, handler func(context.Context, *websocket.Conn)) resourceConfig {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testToken() {
			w.WriteHeader(401)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/console") {
			conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{console.Subprotocol}})
			if err != nil {
				return
			}
			defer conn.CloseNow()
			handler(r.Context(), conn)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"i-00000000000000001","name":"one","state":"running"}`)
	}))
	t.Cleanup(srv.Close)
	return resourceConfig{endpoint: srv.URL, credentialPath: tokenFile(t, testToken())}
}

func runConsoleFiles(ctx context.Context, t *testing.T, cfg resourceConfig, input []byte, terminal consoleTerminal, args ...string) (code int, out, errOut []byte) {
	t.Helper()
	dir := t.TempDir()
	in, err := os.Create(filepath.Join(dir, "stdin"))
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	if _, err = in.Write(input); err != nil {
		t.Fatal(err)
	}
	if _, err = in.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	stdout, err := os.Create(filepath.Join(dir, "stdout"))
	if err != nil {
		t.Fatal(err)
	}
	defer stdout.Close()
	stderr, err := os.Create(filepath.Join(dir, "stderr"))
	if err != nil {
		t.Fatal(err)
	}
	defer stderr.Close()
	code = runConsole(ctx, args, in, stdout, stderr, cfg, terminal)
	out, err = os.ReadFile(stdout.Name())
	if err != nil {
		t.Fatal(err)
	}
	errOut, err = os.ReadFile(stderr.Name())
	if err != nil {
		t.Fatal(err)
	}
	return code, out, errOut
}

func TestConsoleCommandStreams(t *testing.T) {
	wantArgv := []string{"printf", "a b", "$HOME"}
	done := make(chan error, 1)
	cfg := consoleFixture(t, func(ctx context.Context, conn *websocket.Conn) {
		kind, raw, err := conn.Read(ctx)
		if err != nil {
			done <- err
			return
		}
		start, err := console.ParseClientText(raw)
		if kind != websocket.MessageText || err != nil || start.Start.TTY || !reflect.DeepEqual(start.Start.Command, wantArgv) {
			done <- fmt.Errorf("wrong start %s: %w", raw, err)
			return
		}
		var input bytes.Buffer
		for {
			kind, raw, err = conn.Read(ctx)
			if err != nil {
				done <- err
				return
			}
			if kind == websocket.MessageBinary {
				data, err := console.ParseInputFrame(raw)
				if err != nil {
					done <- err
					return
				}
				input.Write(data)
				continue
			}
			control, err := console.ParseClientText(raw)
			if err != nil || control.Type != "stdin_eof" {
				done <- fmt.Errorf("missing EOF %s", raw)
				return
			}
			break
		}
		if !bytes.Equal(input.Bytes(), []byte{'x', 0, 'y'}) {
			done <- fmt.Errorf("stdin=%q", input.Bytes())
			return
		}
		for _, frame := range [][]byte{{1, 'o', 0, 'u', 't'}, {2, 'e', 0, 'r', 'r'}} {
			if err := conn.Write(ctx, websocket.MessageBinary, frame); err != nil {
				done <- err
				return
			}
		}
		if err := conn.Write(ctx, websocket.MessageText, []byte(`{"type":"exit","exit_code":7}`)); err != nil {
			done <- err
			return
		}
		_ = conn.Close(websocket.StatusNormalClosure, "")
		done <- nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	code, out, errOut := runConsoleFiles(ctx, t, cfg, []byte{'x', 0, 'y'}, nil, append([]string{"i-00000000000000001", "--"}, wantArgv...)...)
	if code != 7 || !bytes.Equal(out, []byte{'o', 0, 'u', 't'}) || !bytes.HasSuffix(errOut, []byte{'e', 0, 'r', 'r'}) || !bytes.Contains(errOut, []byte("serial")) {
		t.Fatalf("console code=%d out=%q err=%q", code, out, errOut)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestConsoleExitStatus(t *testing.T) {
	for _, tc := range []struct {
		name   string
		frames []string
		want   int
	}{
		{"zero", []string{`{"type":"exit","exit_code":0}`}, 0},
		{"seven", []string{`{"type":"exit","exit_code":7}`}, 7},
		{"missing", nil, 1},
		{"duplicate", []string{`{"type":"exit","exit_code":0}`, `{"type":"exit","exit_code":0}`}, 1},
		{"invalid", []string{`{"type":"exit","exit_code":256}`}, 1},
		{"runtime-error", []string{`{"type":"error","message":"runtime failed"}`}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := consoleFixture(t, func(ctx context.Context, conn *websocket.Conn) {
				_, _, _ = conn.Read(ctx)
				for _, frame := range tc.frames {
					_ = conn.Write(ctx, websocket.MessageText, []byte(frame))
				}
				_ = conn.Close(websocket.StatusNormalClosure, "")
			})
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			code, _, errOut := runConsoleFiles(ctx, t, cfg, nil, nil, "i-00000000000000001", "--", "true")
			if code != tc.want {
				t.Fatalf("code=%d want=%d err=%q", code, tc.want, errOut)
			}
		})
	}
}

type fakeConsoleTerminal struct {
	mu       sync.Mutex
	restored bool
	sizes    int
}

func (*fakeConsoleTerminal) IsTerminal(*os.File) bool { return true }
func (f *fakeConsoleTerminal) Size(*os.File) (rows, cols uint16, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sizes++
	if f.sizes == 1 {
		return 24, 80, nil
	}
	return 40, 120, nil
}
func (f *fakeConsoleTerminal) Raw(*os.File) (func() error, error) {
	return func() error { f.mu.Lock(); defer f.mu.Unlock(); f.restored = true; return nil }, nil
}

func TestConsoleInteractive(t *testing.T) {
	for _, outcome := range []string{"exit", "error", "interrupt"} {
		t.Run(outcome, func(t *testing.T) {
			terminal := &fakeConsoleTerminal{}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			done := make(chan error, 1)
			cfg := consoleFixture(t, func(ctx context.Context, conn *websocket.Conn) {
				_, raw, err := conn.Read(ctx)
				if err != nil {
					done <- err
					return
				}
				control, err := console.ParseClientText(raw)
				if err != nil || control.Start == nil || !control.Start.TTY || control.Start.Rows != 24 || control.Start.Cols != 80 || !reflect.DeepEqual(control.Start.Command, []string{"/bin/bash"}) {
					done <- fmt.Errorf("start %s %w", raw, err)
					return
				}
				_, raw, err = conn.Read(ctx)
				if err != nil {
					done <- err
					return
				}
				resize, err := console.ParseClientText(raw)
				if err != nil || resize.Resize == nil || resize.Resize.Rows != 40 {
					done <- fmt.Errorf("resize %s %w", raw, err)
					return
				}
				if outcome == "interrupt" {
					cancel()
					_, _, err = conn.Read(ctx)
					if err == nil {
						done <- fmt.Errorf("interrupt left socket open")
						return
					}
					done <- nil
					return
				}
				_ = conn.Write(ctx, websocket.MessageBinary, []byte{1, 's', 0, 'h'})
				frame := console.Exit{Type: "exit", ExitCode: 0}
				raw, _ = json.Marshal(frame)
				if outcome == "error" {
					raw = []byte(`{"type":"error","message":"failed"}`)
				}
				_ = conn.Write(ctx, websocket.MessageText, raw)
				_ = conn.Close(websocket.StatusNormalClosure, "")
				done <- nil
			})
			in, inWriter, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer in.Close()
			defer inWriter.Close()
			dir := t.TempDir()
			out, err := os.Create(filepath.Join(dir, "out"))
			if err != nil {
				t.Fatal(err)
			}
			defer out.Close()
			errFile, err := os.Create(filepath.Join(dir, "err"))
			if err != nil {
				t.Fatal(err)
			}
			defer errFile.Close()
			code := runConsole(ctx, []string{"i-00000000000000001"}, in, out, errFile, cfg, terminal)
			if (outcome == "exit" && code != 0) || (outcome != "exit" && code == 0) {
				t.Fatalf("%s code=%d", outcome, code)
			}
			terminal.mu.Lock()
			restored := terminal.restored
			terminal.mu.Unlock()
			if !restored {
				t.Fatal("terminal not restored")
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}
