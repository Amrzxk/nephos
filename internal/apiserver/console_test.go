package apiserver

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/Amrzxk/nephos/internal/compute"
	"github.com/Amrzxk/nephos/internal/model"
	"github.com/Amrzxk/nephos/internal/service"
	"github.com/Amrzxk/nephos/internal/store"
	"github.com/Amrzxk/nephos/internal/tunnel/console"
	"github.com/Amrzxk/nephos/internal/version"
)

type consoleTestRuntime struct {
	mu       sync.Mutex
	calls    int
	session  *consoleTestSession
	closeErr error
	identity compute.Identity
}

func (r *consoleTestRuntime) Exec(_ context.Context, ref compute.Reference, req compute.ExecRequest) (compute.ExecSession, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if ref.ID != compute.RuntimeID(strings.Repeat("a", 64)) || ref.Identity != r.identity {
		return nil, fmt.Errorf("wrong runtime identity")
	}
	r.calls++
	r.session = newConsoleTestSession(req.TTY)
	r.session.closeErr = r.closeErr
	return r.session, nil
}
func (r *consoleTestRuntime) count() int { r.mu.Lock(); defer r.mu.Unlock(); return r.calls }

type consoleTestSession struct {
	stdinR, stdoutR, stderrR *io.PipeReader
	stdinW, stdoutW, stderrW *io.PipeWriter
	resize                   chan [2]uint16
	done                     chan struct{}
	closed                   chan struct{}
	once                     sync.Once
	closeErr                 error
}

func newConsoleTestSession(tty bool) *consoleTestSession {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	errR, errW := io.Pipe()
	s := &consoleTestSession{stdinR: inR, stdinW: inW, stdoutR: outR, stdoutW: outW,
		stderrR: errR, stderrW: errW, resize: make(chan [2]uint16, 1), done: make(chan struct{}), closed: make(chan struct{})}
	go func() {
		if tty {
			select {
			case <-s.resize:
				_, _ = outW.Write([]byte("terminal\x00"))
			case <-s.closed:
			}
		} else {
			input, _ := io.ReadAll(inR)
			_, _ = outW.Write(append(input, []byte("tail\x00")...))
			_, _ = errW.Write([]byte("error\x00"))
		}
		_ = outW.Close()
		_ = errW.Close()
		close(s.done)
	}()
	return s
}
func (s *consoleTestSession) Stdin() io.WriteCloser { return s.stdinW }
func (s *consoleTestSession) Stdout() io.Reader     { return s.stdoutR }
func (s *consoleTestSession) Stderr() io.Reader     { return s.stderrR }
func (s *consoleTestSession) Resize(_ context.Context, rows, cols uint16) error {
	s.resize <- [2]uint16{rows, cols}
	return nil
}

func TestConsoleCleanupFailureHasNoSuccessExit(t *testing.T) {
	runtime := &consoleTestRuntime{closeErr: fmt.Errorf("remove exec failed")}
	srv, id, _ := consoleTestWorld(t, runtime)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := consoleDial(ctx, srv, id, "secret", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	if err := conn.Write(ctx, websocket.MessageText, []byte(`{"type":"start","command":["cat"],"tty":false}`)); err != nil {
		t.Fatal(err)
	}
	if err := conn.Write(ctx, websocket.MessageText, []byte(`{"type":"stdin_eof"}`)); err != nil {
		t.Fatal(err)
	}
	for {
		kind, raw, err := conn.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if kind == websocket.MessageBinary {
			continue
		}
		control, err := console.ParseServerText(raw)
		if err != nil || control.Error == nil || !strings.Contains(control.Error.Message, "remove exec failed") {
			t.Fatalf("cleanup failure was hidden by success exit: %+v %v", control, err)
		}
		return
	}
}
func (s *consoleTestSession) Wait(ctx context.Context) (int, error) {
	select {
	case <-s.done:
		return 7, nil
	case <-ctx.Done():
		return -1, ctx.Err()
	}
}
func (s *consoleTestSession) Close() error {
	s.once.Do(func() {
		close(s.closed)
		_ = s.stdinW.Close()
		_ = s.stdinR.Close()
		_ = s.stdoutR.Close()
		_ = s.stdoutW.Close()
		_ = s.stderrR.Close()
		_ = s.stderrW.Close()
	})
	return s.closeErr
}

func consoleTestWorld(t *testing.T, runtime *consoleTestRuntime) (server *httptest.Server, runningID, pendingID string) {
	t.Helper()
	ctx := context.Background()
	s, err := store.Open(ctx, filepath.Join(t.TempDir(), "console.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	network := service.NewNetwork(s, nil, nil)
	vpc, err := network.CreateVPC(ctx, service.CreateVPCInput{Name: "vpc", CIDRBlock: "10.0.0.0/16"}, "")
	if err != nil {
		t.Fatal(err)
	}
	subnet, err := network.CreateSubnet(ctx, service.CreateSubnetInput{Name: "subnet", VPCID: vpc.ID, CIDRBlock: "10.0.1.0/24", AvailabilityZone: "local-1a"}, "")
	if err != nil {
		t.Fatal(err)
	}
	instances := service.NewInstances(s, nil, nil)
	running, err := instances.Run(ctx, service.RunInstanceInput{Name: "running", SubnetID: subnet.ID}, "")
	if err != nil {
		t.Fatal(err)
	}
	runtime.identity = compute.Identity{WorkspaceID: "default", InstanceID: running.ID}
	if err := s.RecordRuntimeID(ctx, running.ID, running.Generation, strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	running, err = s.GetInstance(ctx, "default", running.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RecordInstance(ctx, running, model.InstanceRunning, "", true); err != nil {
		t.Fatal(err)
	}
	pending, err := instances.Run(ctx, service.RunInstanceInput{Name: "pending", SubnetID: subnet.ID}, "")
	if err != nil {
		t.Fatal(err)
	}
	h := New("secret", version.Get(), func() bool { return true }, network, instances, s, runtime)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv, running.ID, pending.ID
}
func consoleURL(srv *httptest.Server, id string) string {
	return "ws" + strings.TrimPrefix(srv.URL, "http") + "/v1/workspaces/default/instances/" + id + "/console"
}
func consoleDial(ctx context.Context, srv *httptest.Server, id, token string, extra http.Header) (connection *websocket.Conn, status int, err error) {
	headers := make(http.Header)
	if token != "" {
		headers.Set("Authorization", "Bearer "+token)
	}
	for key, values := range extra {
		headers[key] = values
	}
	connection, response, err := websocket.Dial(ctx, consoleURL(srv, id), &websocket.DialOptions{HTTPHeader: headers, Subprotocols: []string{console.Subprotocol}})
	if response != nil {
		if response.Body != nil {
			_ = response.Body.Close()
		}
		status = response.StatusCode
	}
	return connection, status, err
}

func TestConsoleAuthentication(t *testing.T) {
	runtime := &consoleTestRuntime{}
	srv, runningID, pendingID := consoleTestWorld(t, runtime)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, tc := range []struct {
		name, id, token string
		header          http.Header
		status          int
	}{
		{"absent-token", runningID, "", nil, 401},
		{"wrong-token", runningID, "wrong", nil, 401},
		{"browser-origin", runningID, "secret", http.Header{"Origin": {"https://evil.example"}}, 403},
		{"pending", pendingID, "secret", nil, 409},
		{"missing", "i-00000000000000000", "secret", nil, 404},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conn, resp, err := consoleDial(ctx, srv, tc.id, tc.token, tc.header)
			if conn != nil {
				_ = conn.CloseNow()
			}
			if err == nil || resp != tc.status || runtime.count() != 0 {
				t.Fatalf("handshake conn=%v status=%v err=%v exec=%d", conn, resp, err, runtime.count())
			}
		})
	}
	badHost, hostResponse, hostErr := websocket.Dial(ctx, consoleURL(srv, runningID), &websocket.DialOptions{
		Host: "evil.example", HTTPHeader: http.Header{"Authorization": {"Bearer secret"}}, Subprotocols: []string{console.Subprotocol},
	})
	if badHost != nil {
		_ = badHost.CloseNow()
	}
	if hostResponse != nil {
		_ = hostResponse.Body.Close()
	}
	if hostErr == nil || hostResponse == nil || hostResponse.StatusCode != http.StatusForbidden || runtime.count() != 0 {
		t.Fatalf("wrong Host accepted: response=%v err=%v calls=%d", hostResponse, hostErr, runtime.count())
	}
	conn, _, err := consoleDial(ctx, srv, runningID, "secret", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	if conn.Subprotocol() != console.Subprotocol || runtime.count() != 0 {
		t.Fatalf("upgrade started exec or lost subprotocol: %q %d", conn.Subprotocol(), runtime.count())
	}
	if err := conn.Write(ctx, websocket.MessageText, []byte(`{"type":"start","command":[],"tty":false}`)); err != nil {
		t.Fatal(err)
	}
	_, raw, err := conn.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	control, err := console.ParseServerText(raw)
	if err != nil || control.Type != "error" || runtime.count() != 0 {
		t.Fatalf("invalid start created exec or lacked error: %+v %v calls=%d", control, err, runtime.count())
	}
	_ = conn.CloseNow()
	conn, _, err = consoleDial(ctx, srv, runningID, "secret", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	restURL := srv.URL + "/v1/workspaces/default/instances/" + runningID
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, restURL, http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer secret")
	response, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("terminate between upgrade and start: %d", response.StatusCode)
	}
	if err := conn.Write(ctx, websocket.MessageText, []byte(`{"type":"start","command":["/bin/cat"],"tty":false}`)); err != nil {
		t.Fatal(err)
	}
	_, raw, err = conn.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	control, err = console.ParseServerText(raw)
	if err != nil || control.Type != "error" || runtime.count() != 0 {
		t.Fatalf("stale state created exec: %+v %v calls=%d", control, err, runtime.count())
	}
}

func TestConsoleMissingStart(t *testing.T) {
	runtime := &consoleTestRuntime{}
	srv, id, _ := consoleTestWorld(t, runtime)
	ctx, cancel := context.WithTimeout(context.Background(), 7*time.Second)
	defer cancel()
	conn, _, err := consoleDial(ctx, srv, id, "secret", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	_, _, err = conn.Read(ctx)
	if err == nil || ctx.Err() != nil || runtime.count() != 0 {
		t.Fatalf("missing start did not expire before client timeout: %v exec=%d", err, runtime.count())
	}
}

func TestConsoleOversizedDataReapsSession(t *testing.T) {
	runtime := &consoleTestRuntime{}
	srv, id, _ := consoleTestWorld(t, runtime)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := consoleDial(ctx, srv, id, "secret", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	if err := conn.Write(ctx, websocket.MessageText, []byte(`{"type":"start","command":["cat"],"tty":false}`)); err != nil {
		t.Fatal(err)
	}
	if err := conn.Write(ctx, websocket.MessageBinary, make([]byte, console.MaxDataBytes+1)); err != nil {
		t.Fatal(err)
	}
	_, _, err = conn.Read(ctx)
	if websocket.CloseStatus(err) != websocket.StatusMessageTooBig {
		t.Fatalf("oversized assembled message was not rejected: %v", err)
	}
	runtime.mu.Lock()
	session := runtime.session
	runtime.mu.Unlock()
	if session == nil {
		t.Fatal("valid start did not create exec")
	}
	select {
	case <-session.closed:
	case <-time.After(time.Second):
		t.Fatal("oversized input stranded exec")
	}
}

func TestConsoleProtocolViolations(t *testing.T) {
	for _, tc := range []struct {
		name, first, second string
		tty                 bool
	}{
		{"duplicate-start", `{"type":"start","command":["cat"],"tty":false}`, `{"type":"start","command":["cat"],"tty":false}`, false},
		{"non-tty-resize", `{"type":"start","command":["cat"],"tty":false}`, `{"type":"resize","rows":40,"cols":120}`, false},
		{"tty-stdin-eof", `{"type":"start","command":["bash"],"tty":true,"rows":24,"cols":80}`, `{"type":"stdin_eof"}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runtime := &consoleTestRuntime{}
			srv, id, _ := consoleTestWorld(t, runtime)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			conn, _, err := consoleDial(ctx, srv, id, "secret", nil)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.CloseNow()
			if err := conn.Write(ctx, websocket.MessageText, []byte(tc.first)); err != nil {
				t.Fatal(err)
			}
			if err := conn.Write(ctx, websocket.MessageText, []byte(tc.second)); err != nil {
				t.Fatal(err)
			}
			kind, raw, err := conn.Read(ctx)
			if err != nil || kind != websocket.MessageText {
				t.Fatalf("protocol error frame kind=%v err=%v", kind, err)
			}
			control, err := console.ParseServerText(raw)
			if err != nil || control.Error == nil || runtime.count() != 1 {
				t.Fatalf("invalid control outcome %+v %v calls=%d", control, err, runtime.count())
			}
		})
	}
	for _, first := range []struct {
		name string
		kind websocket.MessageType
		raw  []byte
	}{
		{"binary-before-start", websocket.MessageBinary, []byte{0, 1}},
		{"oversized-control", websocket.MessageText, []byte(strings.Repeat(" ", console.MaxControlBytes+1))},
	} {
		t.Run(first.name, func(t *testing.T) {
			runtime := &consoleTestRuntime{}
			srv, id, _ := consoleTestWorld(t, runtime)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			conn, _, err := consoleDial(ctx, srv, id, "secret", nil)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.CloseNow()
			if err := conn.Write(ctx, first.kind, first.raw); err != nil {
				t.Fatal(err)
			}
			_, raw, err := conn.Read(ctx)
			if err != nil {
				t.Fatal(err)
			}
			control, err := console.ParseServerText(raw)
			if err != nil || control.Error == nil || runtime.count() != 0 {
				t.Fatalf("first frame accepted %+v %v calls=%d", control, err, runtime.count())
			}
		})
	}
}

func TestConsoleDisconnectReapsSession(t *testing.T) {
	runtime := &consoleTestRuntime{}
	srv, id, _ := consoleTestWorld(t, runtime)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := consoleDial(ctx, srv, id, "secret", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.Write(ctx, websocket.MessageText, []byte(`{"type":"start","command":["/bin/cat"],"tty":false}`)); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for runtime.count() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if runtime.count() != 1 {
		t.Fatal("valid start did not create exec")
	}
	_ = conn.CloseNow()
	runtime.mu.Lock()
	session := runtime.session
	runtime.mu.Unlock()
	select {
	case <-session.closed:
	case <-time.After(time.Second):
		t.Fatal("disconnect stranded exec session")
	}
}
