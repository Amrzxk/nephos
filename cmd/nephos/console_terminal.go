package main

import (
	"fmt"
	"os"

	"golang.org/x/term"
)

type consoleTerminal interface {
	IsTerminal(*os.File) bool
	Size(*os.File) (rows, cols uint16, err error)
	Raw(*os.File) (restore func() error, err error)
}

type localConsoleTerminal struct{}

func (localConsoleTerminal) IsTerminal(file *os.File) bool { return term.IsTerminal(int(file.Fd())) }
func (localConsoleTerminal) Size(file *os.File) (rows, cols uint16, err error) {
	width, height, err := term.GetSize(int(file.Fd()))
	if err != nil {
		return 0, 0, fmt.Errorf("read terminal size: %w", err)
	}
	if width <= 0 || height <= 0 || width > 65535 || height > 65535 {
		return 0, 0, fmt.Errorf("terminal dimensions must be 1-65535")
	}
	return uint16(height), uint16(width), nil
}
func (localConsoleTerminal) Raw(file *os.File) (restore func() error, err error) {
	fd := int(file.Fd())
	state, err := term.MakeRaw(fd)
	if err != nil {
		return nil, fmt.Errorf("set raw terminal: %w", err)
	}
	return func() error { return term.Restore(fd, state) }, nil
}
