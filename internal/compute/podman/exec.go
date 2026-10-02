package podman

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Amrzxk/nephos/internal/compute"
)

// Exec attaches one command to a verified running instance.
func (c *Client) Exec(ctx context.Context, ref compute.Reference, req compute.ExecRequest) (compute.ExecSession, error) {
	if err := validateExec(req); err != nil {
		return nil, err
	}
	doc, err := c.inspectOwned(ctx, ref)
	if err != nil {
		return nil, err
	}
	if !doc.State.Running {
		return nil, fmt.Errorf("instance is not running")
	}
	var created struct {
		ID string `json:"Id"`
	}
	config := struct {
		Cmd                                          []string
		Tty, AttachStdin, AttachStdout, AttachStderr bool
	}{
		Cmd: req.Command, Tty: req.TTY, AttachStdin: true, AttachStdout: true, AttachStderr: true}
	if err := c.request(ctx, http.MethodPost, "/containers/"+string(ref.ID)+"/exec", config, &created); err != nil {
		return nil, err
	}
	if created.ID == "" || strings.ContainsAny(created.ID, "/?#\x00") {
		return nil, fmt.Errorf("invalid exec session ID")
	}
	conn, err := c.dial(ctx)
	if err != nil {
		_ = c.removeExec(ctx, created.ID)
		return nil, err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = conn.Close()
			_ = c.removeExec(ctx, created.ID)
		}
	}()
	deadline := time.Now().Add(10 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err := conn.SetDeadline(deadline); err != nil {
		return nil, err
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	raw, err := json.Marshal(struct {
		Detach, Tty   bool
		Height, Width uint16
	}{Tty: req.TTY, Height: req.Rows, Width: req.Cols})
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://podman"+apiPath+"/exec/"+created.ID+"/start", bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Connection", "Upgrade")
	request.Header.Set("Upgrade", "tcp")
	if err := request.Write(conn); err != nil {
		return nil, fmt.Errorf("start exec stream: %w", err)
	}
	reader := bufio.NewReader(conn)
	response, err := http.ReadResponse(reader, request)
	if err != nil {
		return nil, fmt.Errorf("read exec upgrade: %w", err)
	}
	if response.StatusCode != 101 {
		defer func() { _ = response.Body.Close() }()
		detail, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return nil, &APIError{Status: response.StatusCode, Method: http.MethodPost, Path: request.URL.Path, Detail: string(detail)}
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		return nil, err
	}
	unixConn, ok := conn.(*net.UnixConn)
	if !ok {
		return nil, fmt.Errorf("exec requires a Unix connection")
	}
	outR, outW := io.Pipe()
	errR, errW := io.Pipe()
	s := &execSession{client: c, id: created.ID, parent: ctx, conn: unixConn, tty: req.TTY,
		stdout: outR, stderr: errR, outW: outW, errW: errW, done: make(chan struct{})}
	s.stdin = &execInput{conn: unixConn}
	s.stop = context.AfterFunc(ctx, func() { s.closeIO(ctx.Err()) })
	cleanup = false
	go s.readOutput(reader)
	return s, nil
}
func validateExec(req compute.ExecRequest) error {
	if len(req.Command) == 0 || req.Command[0] == "" {
		return fmt.Errorf("exec requires a nonempty command")
	}
	for _, arg := range req.Command {
		if strings.ContainsRune(arg, 0) {
			return fmt.Errorf("exec arguments cannot contain NUL")
		}
	}
	if req.TTY && (req.Rows == 0 || req.Cols == 0) {
		return fmt.Errorf("TTY requires nonzero dimensions")
	}
	if !req.TTY && (req.Rows != 0 || req.Cols != 0) {
		return fmt.Errorf("non-TTY exec cannot have dimensions")
	}
	return nil
}

type execInput struct {
	conn *net.UnixConn
	once sync.Once
	err  error
}

func (i *execInput) Write(data []byte) (int, error) { return i.conn.Write(data) }
func (i *execInput) Close() error                   { i.once.Do(func() { i.err = i.conn.CloseWrite() }); return i.err }

type execSession struct {
	client         *Client
	id             string
	parent         context.Context
	conn           *net.UnixConn
	tty            bool
	stdin          *execInput
	stdout, stderr *io.PipeReader
	outW, errW     *io.PipeWriter
	done           chan struct{}
	stop           func() bool
	once           sync.Once
	streamErr      error // published by close(done)
	closeErr       error // published by sync.Once
}

func (s *execSession) Stdin() io.WriteCloser { return s.stdin }
func (s *execSession) Stdout() io.Reader     { return s.stdout }
func (s *execSession) Stderr() io.Reader     { return s.stderr }
func (s *execSession) readOutput(reader io.Reader) {
	var err error
	if s.tty {
		_ = s.errW.Close()
		_, err = io.Copy(s.outW, reader)
	} else {
		err = demux(reader, s.outW, s.errW)
	}
	s.streamErr = err
	s.outW.CloseWithError(err)
	s.errW.CloseWithError(err)
	close(s.done)
}
func demux(reader io.Reader, stdout, stderr io.Writer) error {
	var header [8]byte
	for {
		_, err := io.ReadFull(reader, header[:])
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("exec stream header: %w", err)
		}
		if header[1] != 0 || header[2] != 0 || header[3] != 0 {
			return fmt.Errorf("invalid exec stream header")
		}
		var writer io.Writer
		switch header[0] {
		case 1:
			writer = stdout
		case 2:
			writer = stderr
		default:
			return fmt.Errorf("invalid exec stream channel %d", header[0])
		}
		size := binary.BigEndian.Uint32(header[4:])
		// CopyN does not allocate based on an untrusted frame length.
		if _, err := io.CopyN(writer, reader, int64(size)); err != nil {
			return fmt.Errorf("exec stream payload: %w", err)
		}
	}
}
func (s *execSession) Resize(ctx context.Context, rows, cols uint16) error {
	if !s.tty || rows == 0 || cols == 0 {
		return fmt.Errorf("resize requires a TTY and nonzero dimensions")
	}
	return s.client.request(ctx, http.MethodPost, "/exec/"+s.id+"/resize?h="+strconv.Itoa(int(rows))+"&w="+strconv.Itoa(int(cols)), nil, nil)
}
func (s *execSession) Wait(ctx context.Context) (int, error) {
	select {
	case <-ctx.Done():
		return -1, ctx.Err()
	case <-s.done:
	}
	if err := ctx.Err(); err != nil {
		return -1, err
	}
	if s.streamErr != nil {
		return -1, s.streamErr
	}
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		var status struct {
			Running  bool
			ExitCode *int
		}
		if err := s.client.request(ctx, http.MethodGet, "/exec/"+s.id+"/json", nil, &status); err != nil {
			return -1, err
		}
		if !status.Running {
			if status.ExitCode == nil || *status.ExitCode < 0 || *status.ExitCode > 255 {
				return -1, fmt.Errorf("exec finished without a valid exit status")
			}
			return *status.ExitCode, nil
		}
		select {
		case <-ctx.Done():
			return -1, ctx.Err()
		case <-ticker.C:
		}
	}
}
func (s *execSession) closeIO(err error) {
	_ = s.conn.Close()
	s.stdout.CloseWithError(err)
	s.stderr.CloseWithError(err)
	s.outW.CloseWithError(err)
	s.errW.CloseWithError(err)
}
func (s *execSession) Close() error {
	s.once.Do(func() {
		s.stop()
		s.closeIO(io.ErrClosedPipe)
		<-s.done
		s.closeErr = s.client.removeExec(s.parent, s.id)
	})
	return s.closeErr
}
func (c *Client) removeExec(parent context.Context, id string) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), 2*time.Second)
	defer cancel()
	// Force stops only this session, never the instance or another session.
	err := c.request(ctx, http.MethodPost, "/exec/"+id+"/remove", struct{ Force bool }{Force: true}, nil)
	if isStatus(err, 404) {
		return nil
	}
	return err
}
