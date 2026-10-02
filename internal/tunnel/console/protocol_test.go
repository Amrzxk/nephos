package console

import (
	"bytes"
	"strings"
	"testing"
)

func TestConsoleProtocol(t *testing.T) {
	start, err := ParseClientText([]byte(`{"type":"start","command":["/bin/bash"],"tty":true,"rows":24,"cols":80}`))
	if err != nil || start.Type != "start" || start.Start == nil || !start.Start.TTY || start.Start.Rows != 24 {
		t.Fatalf("interactive start %+v %v", start, err)
	}
	command, err := ParseClientText([]byte(`{"type":"start","command":["/bin/printf","a b"],"tty":false}`))
	if err != nil || command.Start == nil || command.Start.TTY || command.Start.Command[1] != "a b" {
		t.Fatalf("exact argv %+v %v", command, err)
	}
	for name, raw := range map[string]string{
		"unknown":            `{"type":"start","command":["cat"],"tty":false,"extra":true}`,
		"empty-argv":         `{"type":"start","command":[],"tty":false}`,
		"nul-argv":           `{"type":"start","command":["a\u0000b"],"tty":false}`,
		"missing-tty":        `{"type":"start","command":["cat"]}`,
		"non-tty-dimensions": `{"type":"start","command":["cat"],"tty":false,"rows":0}`,
		"tty-no-dimensions":  `{"type":"start","command":["cat"],"tty":true}`,
		"bad-resize":         `{"type":"resize","rows":0,"cols":80}`,
		"unknown-type":       `{"type":"exit","exit_code":0}`,
		"trailing":           `{"type":"stdin_eof"} {}`,
		"oversized":          strings.Repeat(" ", MaxControlBytes+1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseClientText([]byte(raw)); err == nil {
				t.Fatal("invalid client control accepted")
			}
		})
	}
	if eof, err := ParseClientText([]byte(`{"type":"stdin_eof"}`)); err != nil || eof.Type != "stdin_eof" {
		t.Fatalf("EOF control %+v %v", eof, err)
	}
	if resize, err := ParseClientText([]byte(`{"type":"resize","rows":40,"cols":120}`)); err != nil || resize.Resize == nil || resize.Resize.Cols != 120 {
		t.Fatalf("resize control %+v %v", resize, err)
	}
	frame, err := InputFrame([]byte{0, 1, 2})
	if err != nil || !bytes.Equal(frame, []byte{0, 0, 1, 2}) {
		t.Fatalf("input frame %v %v", frame, err)
	}
	if _, err := ParseInputFrame([]byte{1, 2}); err == nil {
		t.Fatal("wrong-direction client data accepted")
	}
	if _, err := InputFrame(make([]byte, MaxDataBytes)); err == nil {
		t.Fatal("oversized data accepted")
	}
	output, err := OutputFrame(2, []byte{0, 1, 2})
	if err != nil {
		t.Fatal(err)
	}
	channel, data, err := ParseOutputFrame(output)
	if err != nil || channel != 2 || !bytes.Equal(data, []byte{0, 1, 2}) {
		t.Fatalf("output frame %d %v %v", channel, data, err)
	}
	if _, _, err := ParseOutputFrame([]byte{0, 1}); err == nil {
		t.Fatal("wrong-direction server data accepted")
	}
	exit, err := ParseServerText([]byte(`{"type":"exit","exit_code":7}`))
	if err != nil || exit.Exit == nil || exit.Exit.ExitCode != 7 {
		t.Fatalf("nonzero exit %+v %v", exit, err)
	}
	for _, raw := range []string{`{"type":"exit","exit_code":-1}`, `{"type":"exit","exit_code":256}`, `{"type":"exit"}`, `{"type":"error","message":""}`} {
		if _, err := ParseServerText([]byte(raw)); err == nil {
			t.Fatalf("invalid server control accepted: %s", raw)
		}
	}
}
