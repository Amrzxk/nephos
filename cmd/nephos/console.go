package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/Amrzxk/nephos/internal/tunnel/console"
)

func runConsole(ctx context.Context, args []string, stdin, stdout, stderr *os.File, cfg resourceConfig, terminal consoleTerminal) (code int) {
	if len(args) == 0 || args[0] == "" {
		fmt.Fprintln(stderr, "nephos console: instance name or ID required")
		return exitUsage
	}
	start := console.Start{Type: "start", Command: []string{"/bin/bash"}, TTY: true}
	if len(args) > 1 {
		if args[1] != "--" || len(args) < 3 {
			fmt.Fprintln(stderr, "nephos console: use -- followed by a command")
			return exitUsage
		}
		start.Command, start.TTY = args[2:], false
	}
	if terminal == nil {
		terminal = localConsoleTerminal{}
	}
	if start.TTY {
		if !terminal.IsTerminal(stdin) {
			fmt.Fprintln(stderr, "nephos console: interactive mode requires a local terminal; use -- command for redirected I/O")
			return exitUsage
		}
		rows, cols, err := terminal.Size(stdin)
		if err != nil {
			fmt.Fprintf(stderr, "nephos console: %v\n", err)
			return 1
		}
		start.Rows, start.Cols = rows, cols
	}
	raw, err := json.Marshal(start)
	if err == nil {
		_, err = console.ParseClientText(raw)
	}
	if err != nil {
		fmt.Fprintf(stderr, "nephos console: invalid command: %v\n", err)
		return exitUsage
	}
	api, cfg, err := newResourceClient(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "nephos console: %v\n", err)
		return 1
	}
	id, err := resolveInstance(ctx, api, args[0])
	if err != nil {
		fmt.Fprintf(stderr, "nephos console: %v\n", err)
		return 1
	}
	token, err := loadCredential(cfg.credentialPath)
	if err != nil {
		fmt.Fprintf(stderr, "nephos console: %v\n", err)
		return 1
	}
	endpoint, err := url.Parse(cfg.endpoint)
	if err != nil {
		fmt.Fprintln(stderr, "nephos console: invalid API endpoint")
		return 1
	}
	endpoint.Scheme = "ws"
	endpoint.Path = "/v1/workspaces/default/instances/" + id + "/console"
	dialCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	conn, response, err := websocket.Dial(dialCtx, endpoint.String(), &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + token}}, Subprotocols: []string{console.Subprotocol},
		HTTPClient: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
	})
	cancel()
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
	if err != nil {
		if response != nil {
			fmt.Fprintf(stderr, "nephos console: handshake HTTP %d\n", response.StatusCode)
		} else {
			fmt.Fprintln(stderr, "nephos console: connection failed")
		}
		return 1
	}
	defer func() { _ = conn.CloseNow() }()
	if conn.Subprotocol() != console.Subprotocol {
		fmt.Fprintln(stderr, "nephos console: required subprotocol missing")
		return 1
	}
	conn.SetReadLimit(console.MaxDataBytes)
	if start.TTY {
		restore, err := terminal.Raw(stdin)
		if err != nil {
			fmt.Fprintf(stderr, "nephos console: %v\n", err)
			return 1
		}
		defer func() {
			if err := restore(); err != nil {
				fmt.Fprintf(stderr, "nephos console: restore terminal: %v\n", err)
				code = 1
			}
		}()
	}
	if _, err := fmt.Fprintln(stderr, "Nephos serial/exec console (authenticated network-access exception)"); err != nil {
		return 1
	}
	code, err = consoleSession(ctx, conn, raw, start, stdin, stdout, stderr, terminal)
	if err != nil {
		fmt.Fprintf(stderr, "nephos console: %v\n", err)
		return 1
	}
	return code
}

type consoleInput struct {
	data []byte
	err  error
}
type consoleResult struct {
	code int
	err  error
}

func readLocalConsoleInput(ctx context.Context, file *os.File, events chan<- consoleInput) {
	buf := make([]byte, console.MaxDataBytes-1)
	for {
		n, err := readConsoleFile(ctx, file, buf)
		if n > 0 {
			data := append([]byte(nil), buf[:n]...)
			select {
			case events <- consoleInput{data: data}:
			case <-ctx.Done():
				return
			}
		}
		if err != nil {
			select {
			case events <- consoleInput{err: err}:
			case <-ctx.Done():
			}
			return
		}
	}
}

func consoleWrite(ctx context.Context, conn *websocket.Conn, kind websocket.MessageType, raw []byte) error {
	writeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := conn.Write(writeCtx, kind, raw); err != nil {
		return fmt.Errorf("send console input: %w", err)
	}
	return nil
}

func readRemoteConsole(ctx context.Context, conn *websocket.Conn, stdout, stderr io.Writer, tty bool) (int, error) {
	var exit *int
	for {
		kind, raw, err := conn.Read(ctx)
		if err != nil {
			if exit != nil && websocket.CloseStatus(err) == websocket.StatusNormalClosure {
				return *exit, nil
			}
			return 1, fmt.Errorf("console closed without a valid final result: %w", err)
		}
		if exit != nil {
			return 1, fmt.Errorf("console data or duplicate result after final exit")
		}
		switch kind {
		case websocket.MessageBinary:
			channel, data, err := console.ParseOutputFrame(raw)
			if err != nil {
				return 1, err
			}
			writer := stdout
			if channel == 2 {
				if tty {
					return 1, fmt.Errorf("PTY console sent separate stderr")
				}
				writer = stderr
			}
			if _, err := io.Copy(writer, bytes.NewReader(data)); err != nil {
				return 1, fmt.Errorf("write console output: %w", err)
			}
		case websocket.MessageText:
			control, err := console.ParseServerText(raw)
			if err != nil {
				return 1, err
			}
			if control.Error != nil {
				return 1, fmt.Errorf("remote console: %s", control.Error.Message)
			}
			code := control.Exit.ExitCode
			exit = &code
		default:
			return 1, fmt.Errorf("invalid console message type")
		}
	}
}

func consoleSession(parent context.Context, conn *websocket.Conn, startRaw []byte, start console.Start, stdin *os.File, stdout, stderr io.Writer, terminal consoleTerminal) (int, error) {
	ctx, cancel := context.WithCancel(parent)
	var workers sync.WaitGroup
	defer func() { cancel(); _ = conn.CloseNow(); workers.Wait() }()
	if err := consoleWrite(ctx, conn, websocket.MessageText, startRaw); err != nil {
		return 1, err
	}
	input := make(chan consoleInput, 8)
	result := make(chan consoleResult, 1)
	workers.Add(2)
	go func() { defer workers.Done(); readLocalConsoleInput(ctx, stdin, input) }()
	go func() {
		defer workers.Done()
		code, err := readRemoteConsole(ctx, conn, stdout, stderr, start.TTY)
		result <- consoleResult{code: code, err: err}
	}()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	rows, cols := start.Rows, start.Cols
	for {
		select {
		case outcome := <-result:
			return outcome.code, outcome.err
		case event := <-input:
			var raw []byte
			kind := websocket.MessageBinary
			if event.err != nil {
				if !errors.Is(event.err, io.EOF) {
					return 1, fmt.Errorf("read console stdin: %w", event.err)
				}
				if start.TTY {
					return 1, fmt.Errorf("local terminal input closed")
				}
				raw = []byte(`{"type":"stdin_eof"}`)
				kind = websocket.MessageText
				input = nil
			} else {
				var err error
				raw, err = console.InputFrame(event.data)
				if err != nil {
					return 1, err
				}
			}
			if err := consoleWrite(ctx, conn, kind, raw); err != nil {
				// A fast command can close normally while EOF is in flight. The
				// reader still validates the final result and normal closure.
				outcome := <-result
				if outcome.err == nil {
					return outcome.code, nil
				}
				return 1, err
			}
		case <-ticker.C:
			if !start.TTY {
				continue
			}
			nextRows, nextCols, err := terminal.Size(stdin)
			if err != nil {
				return 1, err
			}
			if nextRows == rows && nextCols == cols {
				continue
			}
			raw, err := json.Marshal(console.Resize{Type: "resize", Rows: nextRows, Cols: nextCols})
			if err != nil {
				return 1, err
			}
			if err := consoleWrite(ctx, conn, websocket.MessageText, raw); err != nil {
				return 1, err
			}
			rows, cols = nextRows, nextCols
		case <-ctx.Done():
			return 1, ctx.Err()
		}
	}
}
