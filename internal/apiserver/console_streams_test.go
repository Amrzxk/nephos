package apiserver

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/Amrzxk/nephos/internal/tunnel/console"
)

func TestConsoleCommandStreams(t *testing.T) {
	runtime := &consoleTestRuntime{}
	srv, id, _ := consoleTestWorld(t, runtime)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := consoleDial(ctx, srv, id, "secret", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	if err := conn.Write(ctx, websocket.MessageText, []byte(`{"type":"start","command":["/bin/cat"],"tty":false}`)); err != nil {
		t.Fatal(err)
	}
	input, _ := console.InputFrame([]byte{'x', 0, 'y'})
	if err := conn.Write(ctx, websocket.MessageBinary, input); err != nil {
		t.Fatal(err)
	}
	if err := conn.Write(ctx, websocket.MessageText, []byte(`{"type":"stdin_eof"}`)); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	for {
		kind, raw, err := conn.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if kind == websocket.MessageBinary {
			channel, data, err := console.ParseOutputFrame(raw)
			if err != nil {
				t.Fatal(err)
			}
			if channel == 1 {
				_, _ = stdout.Write(data)
			} else {
				_, _ = stderr.Write(data)
			}
			continue
		}
		control, err := console.ParseServerText(raw)
		if err != nil || control.Exit == nil || control.Exit.ExitCode != 7 {
			t.Fatalf("final exit %+v %v", control, err)
		}
		break
	}
	if stdout.String() != "x\x00ytail\x00" || stderr.String() != "error\x00" || runtime.count() != 1 {
		t.Fatalf("streams stdout=%q stderr=%q exec=%d", stdout.String(), stderr.String(), runtime.count())
	}
	if _, _, err := conn.Read(ctx); websocket.CloseStatus(err) != websocket.StatusNormalClosure {
		t.Fatalf("final exit must be followed by normal closure: %v", err)
	}
}

func TestConsoleTerminalResize(t *testing.T) {
	runtime := &consoleTestRuntime{}
	srv, id, _ := consoleTestWorld(t, runtime)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := consoleDial(ctx, srv, id, "secret", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	if err := conn.Write(ctx, websocket.MessageText, []byte(`{"type":"start","command":["/bin/bash"],"tty":true,"rows":24,"cols":80}`)); err != nil {
		t.Fatal(err)
	}
	if err := conn.Write(ctx, websocket.MessageText, []byte(`{"type":"resize","rows":40,"cols":120}`)); err != nil {
		t.Fatal(err)
	}
	kind, raw, err := conn.Read(ctx)
	if err != nil || kind != websocket.MessageBinary || !bytes.Equal(raw, append([]byte{1}, []byte("terminal\x00")...)) {
		t.Fatalf("PTY output kind=%v raw=%q err=%v", kind, raw, err)
	}
	kind, raw, err = conn.Read(ctx)
	if err != nil || kind != websocket.MessageText {
		t.Fatalf("PTY exit kind=%v err=%v", kind, err)
	}
	control, err := console.ParseServerText(raw)
	if err != nil || control.Exit == nil || control.Exit.ExitCode != 7 {
		t.Fatalf("PTY exit %+v %v", control, err)
	}
}
