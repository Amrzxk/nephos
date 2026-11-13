package podman

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Amrzxk/nephos/internal/compute"
)

type execFixture struct {
	tty          bool
	status       *int
	malformed    bool
	hold         bool
	cleanupError bool
	gotInput     chan []byte
	gotResize    chan string
}

func execServer(t *testing.T, f execFixture) string {
	t.Helper()
	return unixServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && strings.Contains(r.URL.Path, "/containers/"):
			json.NewEncoder(w).Encode(ownedInspect())
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/exec"):
			var config struct {
				Cmd                                          []string
				Tty, AttachStdin, AttachStdout, AttachStderr bool
			}
			json.NewDecoder(r.Body).Decode(&config)
			if !reflect.DeepEqual(config.Cmd, []string{"/bin/fixture", "literal $x"}) || config.Tty != f.tty || !config.AttachStdin || !config.AttachStdout || !config.AttachStderr {
				t.Errorf("exec config %+v", config)
			}
			json.NewEncoder(w).Encode(map[string]string{"Id": "exec-test"})
		case strings.HasSuffix(r.URL.Path, "/start"):
			var body struct {
				Detach, Tty   bool
				Height, Width uint16
			}
			json.NewDecoder(r.Body).Decode(&body)
			if body.Detach || body.Tty != f.tty {
				t.Errorf("start mode %+v", body)
			}
			conn, rw, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			defer conn.Close()
			conn.SetDeadline(time.Now().Add(4 * time.Second))
			rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: tcp\r\n\r\n")
			rw.Flush()
			input, err := io.ReadAll(rw)
			if err != nil {
				return
			}
			if f.gotInput != nil {
				f.gotInput <- input
			}
			if f.hold {
				select {
				case <-r.Context().Done():
				case <-time.After(200 * time.Millisecond):
				}
				return
			}
			if f.malformed {
				conn.Write([]byte{1, 0, 0, 0, 0, 0, 0, 4, 'x'})
				return
			}
			if f.tty {
				conn.Write([]byte("out\x00err\n"))
				return
			}
			for _, chunk := range []struct {
				channel byte
				data    string
			}{{1, "out\x00"}, {2, "err\n"}, {1, "tail"}} {
				header := make([]byte, 8)
				header[0] = chunk.channel
				binary.BigEndian.PutUint32(header[4:], uint32(len(chunk.data)))
				payload := make([]byte, 0, len(header)+len(chunk.data))
				payload = append(payload, header...)
				payload = append(payload, []byte(chunk.data)...)
				for _, b := range payload {
					conn.Write([]byte{b})
				}
			}
		case strings.HasSuffix(r.URL.Path, "/resize"):
			if f.gotResize != nil {
				f.gotResize <- r.URL.RawQuery
			}
			w.WriteHeader(201)
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/json"):
			if f.status == nil {
				json.NewEncoder(w).Encode(map[string]any{"Running": false})
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"Running": false, "ExitCode": *f.status})
		case strings.HasSuffix(r.URL.Path, "/remove"):
			if f.cleanupError {
				http.Error(w, "stop failed", 500)
				return
			}
			w.WriteHeader(200)
		default:
			t.Errorf("unexpected exec %s %s", r.Method, r.URL)
			http.Error(w, "unexpected", 500)
		}
	}))
}
func readExec(t *testing.T, s compute.ExecSession) (stdout, stderr []byte, readErr error) {
	t.Helper()
	var out, errout []byte
	var e1, e2 error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); out, e1 = io.ReadAll(s.Stdout()) }()
	go func() { defer wg.Done(); errout, e2 = io.ReadAll(s.Stderr()) }()
	wg.Wait()
	if e1 != nil {
		return out, errout, e1
	}
	return out, errout, e2
}
func TestPodmanExecStreams(t *testing.T) {
	for _, tty := range []bool{false, true} {
		t.Run(map[bool]string{true: "tty", false: "command"}[tty], func(t *testing.T) {
			code := 7
			input := make(chan []byte, 1)
			resized := make(chan string, 1)
			c := New(execServer(t, execFixture{tty: tty, status: &code, gotInput: input, gotResize: resized}))
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			req := compute.ExecRequest{Command: []string{"/bin/fixture", "literal $x"}, TTY: tty}
			if tty {
				req.Rows = 24
				req.Cols = 80
			}
			s, err := c.Exec(ctx, testReference, req)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			if tty {
				if err := s.Resize(ctx, 40, 120); err != nil {
					t.Fatal(err)
				}
				query := <-resized
				if query != "h=40&w=120" {
					t.Fatalf("resize=%s", query)
				}
			} else if err := s.Resize(ctx, 40, 120); err == nil {
				t.Fatal("non-TTY resized")
			}
			s.Stdin().Write([]byte("in\x00"))
			if err := s.Stdin().Close(); err != nil {
				t.Fatal(err)
			}
			out, errout, err := readExec(t, s)
			if err != nil {
				t.Fatal(err)
			}
			if tty {
				if !bytes.Equal(out, []byte("out\x00err\n")) || len(errout) != 0 {
					t.Fatalf("TTY %q %q", out, errout)
				}
			} else if !bytes.Equal(out, []byte("out\x00tail")) || string(errout) != "err\n" {
				t.Fatalf("streams %q %q", out, errout)
			}
			if string(<-input) != "in\x00" {
				t.Fatal("stdin changed")
			}
			if exit, err := s.Wait(ctx); err != nil || exit != 7 {
				t.Fatalf("exit %d %v", exit, err)
			}
		})
	}
}
func TestPodmanExecExit(t *testing.T) {
	for _, kind := range []string{"missing", "truncated", "cancel"} {
		t.Run(kind, func(t *testing.T) {
			code := 7
			f := execFixture{status: &code}
			switch kind {
			case "missing":
				f.status = nil
			case "truncated":
				f.malformed = true
			case "cancel":
				f.hold = true
			}
			c := New(execServer(t, f))
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			s, err := c.Exec(ctx, testReference, compute.ExecRequest{Command: []string{"/bin/fixture", "literal $x"}})
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			s.Stdin().Close()
			if kind == "cancel" {
				cancel()
			}
			_, _, streamErr := readExec(t, s)
			exit, err := s.Wait(ctx)
			if err == nil {
				t.Fatalf("%s fabricated exit=%d streamErr=%v", kind, exit, streamErr)
			}
		})
	}
}

func TestPodmanExecCleanup(t *testing.T) {
	code := 7
	c := New(execServer(t, execFixture{status: &code, cleanupError: true}))
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	s, err := c.Exec(ctx, testReference, compute.ExecRequest{Command: []string{"/bin/fixture", "literal $x"}})
	if err != nil {
		t.Fatal(err)
	}
	s.Stdin().Close()
	readExec(t, s)
	if err := s.Close(); err == nil || !strings.Contains(err.Error(), "stop failed") {
		t.Fatalf("cleanup failure hidden: %v", err)
	}
}
