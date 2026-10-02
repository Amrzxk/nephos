package apiserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/Amrzxk/nephos/internal/apiserver/generated"
	"github.com/Amrzxk/nephos/internal/compute"
	"github.com/Amrzxk/nephos/internal/model"
	"github.com/Amrzxk/nephos/internal/tunnel/console"
)

func offersConsoleProtocol(header string) bool {
	for _, offered := range strings.Split(header, ",") {
		if strings.TrimSpace(offered) == console.Subprotocol {
			return true
		}
	}
	return false
}

func consoleReady(instance model.Instance) bool {
	return !instance.DeletionRequested && instance.State == model.InstanceRunning &&
		instance.ObservedGeneration == instance.Generation && instance.RuntimeID != ""
}

func (s *server) GetInstanceConsole(w http.ResponseWriter, r *http.Request, workspace generated.Workspace, id generated.ResourceID) {
	if !checkWorkspace(w, workspace) || !s.checkInstances(w) {
		return
	}
	if s.console == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "ServiceUnavailable", "console runtime is not ready", "")
		return
	}
	instance, err := s.instances.Get(r.Context(), id)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	if !consoleReady(instance) {
		writeAPIError(w, http.StatusConflict, "IncorrectState", "instance is not running", id)
		return
	}
	if !offersConsoleProtocol(r.Header.Get("Sec-WebSocket-Protocol")) {
		writeAPIError(w, http.StatusBadRequest, "InvalidParameterValue", "nephos.console.v1 subprotocol is required", id)
		return
	}
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{console.Subprotocol}})
	if err != nil {
		return // Accept wrote the handshake error.
	}
	defer func() { _ = conn.CloseNow() }()
	conn.SetReadLimit(console.MaxDataBytes)
	if conn.Subprotocol() != console.Subprotocol {
		_ = conn.Close(websocket.StatusPolicyViolation, "console subprotocol required")
		return
	}
	readCtx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	kind, raw, err := conn.Read(readCtx)
	cancel()
	if err != nil {
		writeConsoleError(r.Context(), conn, fmt.Errorf("console start missing: %w", err))
		return
	}
	if kind != websocket.MessageText {
		writeConsoleError(r.Context(), conn, fmt.Errorf("first console message must be start text"))
		return
	}
	control, err := console.ParseClientText(raw)
	if err != nil || control.Type != "start" || control.Start == nil {
		writeConsoleError(r.Context(), conn, fmt.Errorf("invalid console start frame"))
		return
	}
	// The instance may have terminated while the client prepared its start.
	instance, err = s.instances.Get(r.Context(), id)
	if err != nil || !consoleReady(instance) {
		writeConsoleError(r.Context(), conn, fmt.Errorf("instance is no longer running"))
		return
	}
	start := control.Start
	ref := compute.Reference{Identity: compute.Identity{WorkspaceID: instance.WorkspaceID, InstanceID: instance.ID}, ID: compute.RuntimeID(instance.RuntimeID)}
	session, err := s.console.Exec(r.Context(), ref, compute.ExecRequest{
		Command: start.Command, TTY: start.TTY, Rows: start.Rows, Cols: start.Cols,
	})
	if err != nil {
		writeConsoleError(r.Context(), conn, err)
		return
	}
	if err := bridgeConsole(r.Context(), conn, session, start.TTY); err != nil {
		return // The bridge reports the error before canceling its reader.
	}
}

func writeConsoleError(ctx context.Context, conn *websocket.Conn, err error) {
	message := err.Error()
	if len(message) > 2048 {
		message = message[:2048]
	}
	raw, marshalErr := json.Marshal(console.Error{Type: "error", Message: message})
	if marshalErr == nil {
		writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		_ = conn.Write(writeCtx, websocket.MessageText, raw)
		cancel()
	}
	_ = conn.Close(websocket.StatusPolicyViolation, "console error")
}

type consoleEvent struct {
	channel byte
	data    []byte
	exit    *int
	err     error
}

func sendConsoleEvent(ctx context.Context, events chan<- consoleEvent, event consoleEvent) {
	select {
	case events <- event:
	case <-ctx.Done():
	}
}

func pumpConsoleOutput(ctx context.Context, source io.Reader, channel byte, events chan<- consoleEvent) {
	buf := make([]byte, console.MaxDataBytes-1)
	for {
		n, err := source.Read(buf)
		if n > 0 {
			chunk := append([]byte(nil), buf[:n]...)
			sendConsoleEvent(ctx, events, consoleEvent{channel: channel, data: chunk})
		}
		if errors.Is(err, io.EOF) {
			return
		}
		if err != nil {
			sendConsoleEvent(ctx, events, consoleEvent{err: fmt.Errorf("read console output: %w", err)})
			return
		}
	}
}

func readConsoleInput(ctx context.Context, conn *websocket.Conn, session compute.ExecSession, tty bool) error {
	eof := false
	for {
		kind, raw, err := conn.Read(ctx)
		if err != nil {
			return fmt.Errorf("console input disconnected: %w", err)
		}
		switch kind {
		case websocket.MessageBinary:
			data, err := console.ParseInputFrame(raw)
			if err != nil || eof {
				return fmt.Errorf("invalid console stdin frame")
			}
			for len(data) > 0 {
				n, err := session.Stdin().Write(data)
				if err != nil {
					return fmt.Errorf("write console stdin: %w", err)
				}
				if n <= 0 || n > len(data) {
					return fmt.Errorf("write console stdin: %w", io.ErrShortWrite)
				}
				data = data[n:]
			}
		case websocket.MessageText:
			control, err := console.ParseClientText(raw)
			if err != nil {
				return err
			}
			switch control.Type {
			case "stdin_eof":
				if tty || eof {
					return fmt.Errorf("stdin_eof requires open non-TTY stdin")
				}
				eof = true
				if err := session.Stdin().Close(); err != nil {
					return fmt.Errorf("close console stdin: %w", err)
				}
			case "resize":
				if !tty {
					return fmt.Errorf("resize requires TTY mode")
				}
				if err := session.Resize(ctx, control.Resize.Rows, control.Resize.Cols); err != nil {
					return fmt.Errorf("resize console: %w", err)
				}
			default:
				return fmt.Errorf("duplicate or invalid console start")
			}
		default:
			return fmt.Errorf("invalid console message type")
		}
	}
}

func writeConsoleMessage(ctx context.Context, conn *websocket.Conn, kind websocket.MessageType, raw []byte) error {
	writeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := conn.Write(writeCtx, kind, raw); err != nil {
		return fmt.Errorf("write console message: %w", err)
	}
	return nil
}

func bridgeConsole(parent context.Context, conn *websocket.Conn, session compute.ExecSession, tty bool) (result error) {
	ctx, cancel := context.WithCancel(parent)
	events := make(chan consoleEvent, 8)
	inputDone := make(chan error, 1)
	var workers sync.WaitGroup
	workers.Add(1)
	go func() { defer workers.Done(); inputDone <- readConsoleInput(ctx, conn, session, tty) }()
	var output sync.WaitGroup
	for _, stream := range []struct {
		reader  io.Reader
		channel byte
	}{{session.Stdout(), 1}, {session.Stderr(), 2}} {
		if tty && stream.channel == 2 {
			continue
		}
		output.Add(1)
		workers.Add(1)
		go func() {
			defer workers.Done()
			defer output.Done()
			pumpConsoleOutput(ctx, stream.reader, stream.channel, events)
		}()
	}
	workers.Add(1)
	go func() {
		defer workers.Done()
		output.Wait()
		code, err := session.Wait(ctx)
		if err != nil {
			sendConsoleEvent(ctx, events, consoleEvent{err: fmt.Errorf("wait for console exit: %w", err)})
			return
		}
		if code < 0 || code > 255 {
			sendConsoleEvent(ctx, events, consoleEvent{err: fmt.Errorf("console returned an invalid exit status")})
			return
		}
		sendConsoleEvent(ctx, events, consoleEvent{exit: &code})
	}()
	defer func() {
		if result != nil {
			writeConsoleError(parent, conn, result)
		}
		cancel()
		_ = session.Close()
		workers.Wait()
	}()
	for {
		select {
		case err := <-inputDone:
			return err
		case event := <-events:
			if event.err != nil {
				return event.err
			}
			if event.exit != nil {
				if err := session.Close(); err != nil {
					return fmt.Errorf("close console session: %w", err)
				}
				raw, err := json.Marshal(console.Exit{Type: "exit", ExitCode: *event.exit})
				if err != nil {
					return err
				}
				if err := writeConsoleMessage(ctx, conn, websocket.MessageText, raw); err != nil {
					return err
				}
				// Complete the normal close handshake while the input reader is
				// still alive. Canceling Read first closes the socket abruptly.
				if err := conn.Close(websocket.StatusNormalClosure, ""); err != nil {
					return fmt.Errorf("close console connection: %w", err)
				}
				return nil
			}
			if tty && event.channel == 2 {
				return fmt.Errorf("TTY emitted a separate stderr stream")
			}
			raw, err := console.OutputFrame(event.channel, event.data)
			if err != nil {
				return err
			}
			if err := writeConsoleMessage(ctx, conn, websocket.MessageBinary, raw); err != nil {
				return err
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
