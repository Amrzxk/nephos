package main

import (
	"context"
	"fmt"
	"os"
	"time"
)

// The console owns its stream handles, not the caller's stdout/stderr. A
// pending local write has the same bounded lifetime as a WebSocket write;
// waiting for remote output does not impose an interactive idle timeout.
type consoleOutput struct {
	ctx     context.Context
	file    *os.File
	release func() error
}

func newConsoleOutput(ctx context.Context, file *os.File) (*consoleOutput, error) {
	owned, release, err := openConsoleOutput(file)
	if err != nil {
		return nil, fmt.Errorf("prepare console output: %w", err)
	}
	return &consoleOutput{ctx: ctx, file: owned, release: release}, nil
}

func (output *consoleOutput) Write(data []byte) (int, error) {
	ctx, cancel := context.WithTimeout(output.ctx, 10*time.Second)
	defer cancel()
	return writeConsoleFile(ctx, output.file, data)
}

func (output *consoleOutput) Close() error { return output.release() }
