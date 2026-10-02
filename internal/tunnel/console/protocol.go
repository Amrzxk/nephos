// Package console defines the versioned, shared instance-console wire format.
package console

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

const (
	// Subprotocol must be negotiated by every console WebSocket.
	Subprotocol = "nephos.console.v1"
	// MaxControlBytes bounds each complete JSON control message.
	MaxControlBytes = 4 * 1024
	// MaxDataBytes includes the direction/channel byte in a binary message.
	MaxDataBytes = 64 * 1024
)

// Start requests an exact argv; non-TTY commands omit dimensions.
type Start struct {
	Type    string   `json:"type"`
	Command []string `json:"command"`
	TTY     bool     `json:"tty"`
	Rows    uint16   `json:"rows,omitempty"`
	Cols    uint16   `json:"cols,omitempty"`
}

// Resize changes an interactive PTY's size.
type Resize struct {
	Type string `json:"type"`
	Rows uint16 `json:"rows"`
	Cols uint16 `json:"cols"`
}

// StdinEOF half-closes non-TTY stdin while output continues.
type StdinEOF struct {
	Type string `json:"type"`
}

// Exit is sent only after output drains and a valid runtime exit status exists.
type Exit struct {
	Type     string `json:"type"`
	ExitCode int    `json:"exit_code"`
}

// Error reports a protocol or runtime failure, never a successful exit.
type Error struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// ClientControl is one validated client text message.
type ClientControl struct {
	Type   string
	Start  *Start
	Resize *Resize
}

// ServerControl is one validated server text message.
type ServerControl struct {
	Type  string
	Exit  *Exit
	Error *Error
}

func strictControl(raw []byte, target any) error {
	if len(raw) == 0 || len(raw) > MaxControlBytes {
		return fmt.Errorf("console control exceeds size limit or is empty")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode console control: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return fmt.Errorf("console control contains trailing data")
	}
	return nil
}
func controlType(raw []byte) (string, error) {
	if len(raw) == 0 || len(raw) > MaxControlBytes {
		return "", fmt.Errorf("console control exceeds size limit or is empty")
	}
	var head struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		return "", fmt.Errorf("decode console control type: %w", err)
	}
	if head.Type == "" {
		return "", fmt.Errorf("console control type is required")
	}
	return head.Type, nil
}

// ParseClientText rejects unknown fields, invalid argv, and wrong-mode sizes.
func ParseClientText(raw []byte) (ClientControl, error) {
	kind, err := controlType(raw)
	if err != nil {
		return ClientControl{}, err
	}
	switch kind {
	case "start":
		var wire struct {
			Type    string   `json:"type"`
			Command []string `json:"command"`
			TTY     *bool    `json:"tty"`
			Rows    *uint16  `json:"rows"`
			Cols    *uint16  `json:"cols"`
		}
		if err := strictControl(raw, &wire); err != nil {
			return ClientControl{}, err
		}
		if wire.TTY == nil || len(wire.Command) == 0 || wire.Command[0] == "" {
			return ClientControl{}, fmt.Errorf("console start requires tty and nonempty argv")
		}
		for _, arg := range wire.Command {
			if strings.ContainsRune(arg, 0) {
				return ClientControl{}, fmt.Errorf("console argv contains NUL")
			}
		}
		start := Start{Type: kind, Command: wire.Command, TTY: *wire.TTY}
		if start.TTY {
			if wire.Rows == nil || wire.Cols == nil || *wire.Rows == 0 || *wire.Cols == 0 {
				return ClientControl{}, fmt.Errorf("TTY start requires nonzero dimensions")
			}
			start.Rows, start.Cols = *wire.Rows, *wire.Cols
		} else if wire.Rows != nil || wire.Cols != nil {
			return ClientControl{}, fmt.Errorf("non-TTY start must omit dimensions")
		}
		return ClientControl{Type: kind, Start: &start}, nil
	case "resize":
		var wire struct {
			Type string  `json:"type"`
			Rows *uint16 `json:"rows"`
			Cols *uint16 `json:"cols"`
		}
		if err := strictControl(raw, &wire); err != nil {
			return ClientControl{}, err
		}
		if wire.Rows == nil || wire.Cols == nil || *wire.Rows == 0 || *wire.Cols == 0 {
			return ClientControl{}, fmt.Errorf("resize requires nonzero dimensions")
		}
		resize := Resize{Type: kind, Rows: *wire.Rows, Cols: *wire.Cols}
		return ClientControl{Type: kind, Resize: &resize}, nil
	case "stdin_eof":
		var wire StdinEOF
		if err := strictControl(raw, &wire); err != nil {
			return ClientControl{}, err
		}
		return ClientControl{Type: kind}, nil
	default:
		return ClientControl{}, fmt.Errorf("unsupported client control %q", kind)
	}
}

// ParseServerText rejects missing or invalid terminal outcomes.
func ParseServerText(raw []byte) (ServerControl, error) {
	kind, err := controlType(raw)
	if err != nil {
		return ServerControl{}, err
	}
	switch kind {
	case "exit":
		var wire struct {
			Type     string `json:"type"`
			ExitCode *int   `json:"exit_code"`
		}
		if err := strictControl(raw, &wire); err != nil {
			return ServerControl{}, err
		}
		if wire.ExitCode == nil || *wire.ExitCode < 0 || *wire.ExitCode > 255 {
			return ServerControl{}, fmt.Errorf("invalid console exit status")
		}
		exit := Exit{Type: kind, ExitCode: *wire.ExitCode}
		return ServerControl{Type: kind, Exit: &exit}, nil
	case "error":
		var wire Error
		if err := strictControl(raw, &wire); err != nil {
			return ServerControl{}, err
		}
		if wire.Message == "" {
			return ServerControl{}, fmt.Errorf("console error message is empty")
		}
		return ServerControl{Type: kind, Error: &wire}, nil
	default:
		return ServerControl{}, fmt.Errorf("unsupported server control %q", kind)
	}
}

func frame(channel byte, data []byte) ([]byte, error) {
	if len(data)+1 > MaxDataBytes {
		return nil, fmt.Errorf("console data frame exceeds %d bytes", MaxDataBytes)
	}
	result := make([]byte, len(data)+1)
	result[0] = channel
	copy(result[1:], data)
	return result, nil
}

// InputFrame wraps raw stdin bytes in the client-only channel.
func InputFrame(data []byte) ([]byte, error) { return frame(0, data) }

// ParseInputFrame accepts only channel zero and a bounded message.
func ParseInputFrame(raw []byte) ([]byte, error) {
	if len(raw) == 0 || len(raw) > MaxDataBytes || raw[0] != 0 {
		return nil, fmt.Errorf("invalid console stdin frame")
	}
	return raw[1:], nil
}

// OutputFrame wraps one stdout or stderr chunk for the client.
func OutputFrame(channel byte, data []byte) ([]byte, error) {
	if channel != 1 && channel != 2 {
		return nil, fmt.Errorf("invalid console output channel")
	}
	return frame(channel, data)
}

// ParseOutputFrame accepts only bounded stdout or stderr messages.
func ParseOutputFrame(raw []byte) (channel byte, data []byte, err error) {
	if len(raw) == 0 || len(raw) > MaxDataBytes || (raw[0] != 1 && raw[0] != 2) {
		return 0, nil, fmt.Errorf("invalid console output frame")
	}
	return raw[0], raw[1:], nil
}
