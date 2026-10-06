package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/Amrzxk/nephos/internal/compute"
	"github.com/Amrzxk/nephos/internal/lifecycle"
	"github.com/Amrzxk/nephos/internal/model"
	"github.com/Amrzxk/nephos/internal/service"
	"github.com/Amrzxk/nephos/internal/store"
	"github.com/Amrzxk/nephos/internal/tunnel/console"
	"github.com/Amrzxk/nephos/internal/version"
)

type shutdownSession struct {
	inR, outR, errR *io.PipeReader
	inW, outW, errW *io.PipeWriter
	closed, closing chan struct{}
	release         <-chan struct{}
	once            sync.Once
	err             error
	resizeEntered   chan struct{}
	resizeOnce      sync.Once
}

func newShutdownSession(release <-chan struct{}, err error) *shutdownSession {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	errR, errW := io.Pipe()
	return &shutdownSession{inR: inR, inW: inW, outR: outR, outW: outW, errR: errR, errW: errW,
		closed: make(chan struct{}), closing: make(chan struct{}), release: release, err: err}
}
func (s *shutdownSession) Stdin() io.WriteCloser { return s.inW }
func (s *shutdownSession) Stdout() io.Reader     { return s.outR }
func (s *shutdownSession) Stderr() io.Reader     { return s.errR }
func (s *shutdownSession) Resize(ctx context.Context, _, _ uint16) error {
	if s.resizeEntered == nil {
		return nil
	}
	s.resizeOnce.Do(func() { close(s.resizeEntered) })
	<-ctx.Done()
	return ctx.Err()
}
func (s *shutdownSession) Wait(ctx context.Context) (int, error) {
	<-ctx.Done()
	return -1, ctx.Err()
}
func (s *shutdownSession) Close() error {
	s.once.Do(func() {
		close(s.closing)
		if s.release != nil {
			<-s.release
		}
		_ = s.inR.Close()
		_ = s.inW.Close()
		_ = s.outR.Close()
		_ = s.outW.Close()
		_ = s.errR.Close()
		_ = s.errW.Close()
		close(s.closed)
	})
	return s.err
}

type shutdownRuntime struct {
	blockingInstanceRuntime
	sessions      chan *shutdownSession
	release       <-chan struct{}
	closeErr      error
	stopped       chan compute.Reference
	stopCheck     func() error
	stopObserver  func(context.Context, compute.Reference, time.Duration) error
	resizeEntered chan struct{}
}

func (r *shutdownRuntime) Lookup(_ context.Context, identity compute.Identity) (compute.Reference, error) {
	return compute.Reference{Identity: identity, ID: compute.RuntimeID(strings.Repeat("a", 64))}, nil
}
func (r *shutdownRuntime) Exec(_ context.Context, _ compute.Reference, _ compute.ExecRequest) (compute.ExecSession, error) {
	s := newShutdownSession(r.release, r.closeErr)
	s.resizeEntered = r.resizeEntered
	r.sessions <- s
	return s, nil
}
func (r *shutdownRuntime) Stop(ctx context.Context, ref compute.Reference, timeout time.Duration) error {
	if r.stopObserver != nil {
		return r.stopObserver(ctx, ref, timeout)
	}
	if r.stopCheck != nil {
		if err := r.stopCheck(); err != nil {
			return err
		}
	}
	select {
	case r.stopped <- ref:
	default:
	}
	return nil
}

type daemonWorld struct {
	listener               net.Listener
	cancel                 context.CancelFunc
	done                   <-chan error
	url, id, token, dbPath string
	runtime                *shutdownRuntime
}

func newDaemonWorld(t *testing.T, release <-chan struct{}, closeErr error, options ...daemonOptions) daemonWorld {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	var config net.ListenConfig
	ln, err := config.Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	dir := t.TempDir()
	runtimeClient := &shutdownRuntime{sessions: make(chan *shutdownSession, 4), release: release, closeErr: closeErr, stopped: make(chan compute.Reference, 4)}
	network := &blockingNetwork{entered: make(chan struct{}, 1), release: make(chan struct{})}
	close(network.release)
	done := make(chan error, 1)
	go func() {
		done <- serve(ctx, ln, filepath.Join(dir, "token"), filepath.Join(dir, "state.db"), filepath.Join(dir, "hook.sock"), version.Get(), network, runtimeClient, options...)
	}()
	url := "http://" + ln.Addr().String()
	client := newTestHTTPClient(t)
	deadline := time.After(3 * time.Second)
	for {
		resp, err := client.Get(url + "/v1/health")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				break
			}
		}
		select {
		case <-deadline:
			t.Fatal("daemon not ready")
		case <-time.After(time.Millisecond):
		}
	}
	client.CloseIdleConnections()
	// Populate after the initial sweep; no queue hint or real kernel work is
	// needed to test ownership of an already admitted console.
	s, err := store.Open(ctx, filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	netService := service.NewNetwork(s, nil, nil)
	vpc, err := netService.CreateVPC(ctx, service.CreateVPCInput{Name: "shutdown", CIDRBlock: "10.0.0.0/16"}, "")
	if err != nil {
		t.Fatal(err)
	}
	subnet, err := netService.CreateSubnet(ctx, service.CreateSubnetInput{Name: "shutdown", VPCID: vpc.ID, CIDRBlock: "10.0.1.0/24", AvailabilityZone: "local-1a"}, "")
	if err != nil {
		t.Fatal(err)
	}
	instance, err := service.NewInstances(s, nil, nil).Run(ctx, service.RunInstanceInput{Name: "shutdown", SubnetID: subnet.ID}, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RecordRuntimeID(ctx, instance.ID, instance.Generation, strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	instance, err = s.GetInstance(ctx, "default", instance.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RecordInstance(ctx, instance, model.InstanceRunning, "", true); err != nil {
		t.Fatal(err)
	}
	token, err := os.ReadFile(filepath.Join(dir, "token"))
	if err != nil {
		t.Fatal(err)
	}
	return daemonWorld{listener: ln, cancel: cancel, done: done, url: url, id: instance.ID, token: strings.TrimSpace(string(token)), runtime: runtimeClient, dbPath: filepath.Join(dir, "state.db")}
}

func (w daemonWorld) console(t *testing.T, tty bool) *shutdownSession {
	t.Helper()
	s, _ := w.startConsole(t, tty)
	return s
}

func (w daemonWorld) startConsole(t *testing.T, tty bool) (*shutdownSession, *websocket.Conn) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, resp, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(w.url, "http")+"/v1/workspaces/default/instances/"+w.id+"/console", &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + w.token}}, Subprotocols: []string{console.Subprotocol}})
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.CloseNow() })
	start := `{"type":"start","command":["sleep","infinity"],"tty":false}`
	if tty {
		start = `{"type":"start","command":["sleep","infinity"],"tty":true,"rows":24,"cols":80}`
	}
	if err := conn.Write(ctx, websocket.MessageText, []byte(start)); err != nil {
		t.Fatal(err)
	}
	select {
	case s := <-w.runtime.sessions:
		return s, conn
	case <-ctx.Done():
		t.Fatal("exec was not admitted")
		return nil, nil
	}
}

func TestConsoleDaemonShutdownJoinsPendingResize(t *testing.T) {
	w := newDaemonWorld(t, nil, nil)
	w.runtime.resizeEntered = make(chan struct{})
	s, conn := w.startConsole(t, true)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := conn.Write(ctx, websocket.MessageText, []byte(`{"type":"resize","rows":40,"cols":100}`)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-s.resizeEntered:
	case <-ctx.Done():
		t.Fatal("resize did not block")
	}
	w.cancel()
	select {
	case err := <-w.done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("pending resize stranded shutdown")
	}
	select {
	case <-s.closed:
	default:
		t.Fatal("pending resize exec was not closed")
	}
}

func TestNestedStopBudgetIsSharedAcrossInstances(t *testing.T) {
	w := newDaemonWorld(t, nil, nil)
	s, err := store.Open(context.Background(), w.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	first, err := s.GetInstance(context.Background(), "default", w.id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.NewInstances(s, nil, nil).Run(context.Background(), service.RunInstanceInput{Name: "second", SubnetID: first.SubnetID}, ""); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	deadline, _ := ctx.Deadline()
	calls := 0
	w.runtime.stopObserver = func(stopCtx context.Context, _ compute.Reference, timeout time.Duration) error {
		calls++
		got, _ := stopCtx.Deadline()
		if !got.Equal(deadline) || timeout > 100*time.Millisecond {
			t.Errorf("per-instance budget replaced shared deadline: deadline=%v timeout=%s", got, timeout)
		}
		<-stopCtx.Done()
		return stopCtx.Err()
	}
	if err := stopNested(ctx, s, w.runtime); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("bounded Stop failure hidden: %v", err)
	}
	if calls == 0 {
		t.Fatal("nested Stop was not attempted")
	}
	w.runtime.stopObserver = nil
	w.cancel()
	select {
	case err := <-w.done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("fixture daemon did not stop")
	}
}

func TestConsoleDaemonShutdownJoins(t *testing.T) {
	for _, tty := range []bool{false, true} {
		t.Run(fmt.Sprint("tty=", tty), func(t *testing.T) {
			w := newDaemonWorld(t, nil, nil)
			a, b := w.console(t, tty), w.console(t, tty)
			w.cancel()
			select {
			case err := <-w.done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("daemon did not drain consoles")
			}
			for _, s := range []*shutdownSession{a, b} {
				select {
				case <-s.closed:
				default:
					t.Fatal("daemon returned with a live console exec")
				}
			}
			select {
			case ref := <-w.runtime.stopped:
				if ref.Identity.InstanceID != w.id {
					t.Fatal("stopped wrong instance")
				}
			default:
				t.Fatal("nested instance was not stopped before daemon returned")
			}
		})
	}
}

func TestDaemonFailureDrainsSessions(t *testing.T) {
	w := newDaemonWorld(t, nil, errors.New("exec cleanup failed"), daemonOptions{shutdownBudget: 100 * time.Millisecond})
	s := w.console(t, false)
	_ = w.listener.Close() // An unexpected API exit must take the same cleanup path.
	select {
	case err := <-w.done:
		if err == nil || !strings.Contains(err.Error(), "exec cleanup failed") {
			t.Fatalf("cleanup failure lost: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("failed daemon did not drain")
	}
	select {
	case <-s.closed:
	default:
		t.Fatal("failed daemon left its exec live")
	}
}

func TestNestedStopPrecedesRuntimeShutdown(t *testing.T) {
	w := newDaemonWorld(t, nil, nil)
	s := w.console(t, false)
	w.runtime.stopCheck = func() error {
		select {
		case <-s.closed:
			return nil
		default:
			return errors.New("nested Stop preceded exec cleanup")
		}
	}
	w.cancel()
	select {
	case err := <-w.done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("shutdown did not join")
	}
	select {
	case <-w.runtime.stopped:
	default:
		t.Fatal("daemon returned before Stop (entrypoint would now kill Podman)")
	}
}

func TestConsoleDaemonShutdownJoinsDeferredClose(t *testing.T) {
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	w := newDaemonWorld(t, release, nil)
	s := w.console(t, false)
	w.cancel()
	select {
	case <-s.closing:
	case <-time.After(3 * time.Second):
		t.Fatal("exec close never requested")
	}
	select {
	case err := <-w.done:
		t.Fatalf("daemon returned before deferred exec cleanup: %v", err)
	default:
	}
	select {
	case <-w.runtime.stopped:
		t.Fatal("nested Stop raced exec cleanup")
	default:
	}
	unblock()
	select {
	case err := <-w.done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("daemon did not join released close")
	}
}

type pendingStartRuntime struct {
	compute.Runtime
	entered, release chan struct{}
}

func (r *pendingStartRuntime) Start(ctx context.Context, _ compute.Reference) error {
	close(r.entered)
	select {
	case <-r.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func TestStartHookChildCompletesDuringDrain(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	gate := lifecycle.NewGate()
	parent, err := gate.Enter(ctx, "default")
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Close()
	runtimeClient := &pendingStartRuntime{entered: make(chan struct{}), release: make(chan struct{})}
	p := &pendingStarts{Runtime: runtimeClient, gate: gate, parents: make(map[compute.Reference]*lifecycle.Lease)}
	ref := compute.Reference{Identity: compute.Identity{WorkspaceID: "default", InstanceID: "i-00000000000000001"}, ID: compute.RuntimeID(strings.Repeat("a", 64))}
	started := make(chan error, 1)
	go func() { started <- p.Start(lifecycle.WithLease(ctx, parent), ref) }()
	<-runtimeClient.entered
	entered := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- gate.Exclusive(ctx, "default", func(context.Context) error { close(entered); return nil })
	}()
	for {
		lease, err := gate.Enter(ctx, "default")
		if errors.Is(err, lifecycle.ErrDraining) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		_ = lease.Close()
		runtime.Gosched()
	}
	wrong := ref
	wrong.ID = compute.RuntimeID(strings.Repeat("b", 64))
	if _, err := p.admitHook(ctx, wrong); err == nil {
		t.Fatal("forged runtime binding admitted")
	}
	release, err := p.admitHook(ctx, ref)
	if err != nil {
		t.Fatalf("verified Start child blocked during drain: %v", err)
	}
	close(runtimeClient.release)
	if err := <-started; err != nil {
		t.Fatal(err)
	}
	_ = parent.Close()
	select {
	case <-entered:
		t.Fatal("Start return erased live hook family")
	default:
	}
	if _, err := p.admitHook(ctx, ref); err == nil {
		t.Fatal("finished Start admitted another hook")
	}
	_ = release()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
