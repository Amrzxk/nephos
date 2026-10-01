//go:build linux || darwin

package main

import (
	"context"
	"errors"
	"os"
	"time"
)

func writeConsoleFile(ctx context.Context, file *os.File, data []byte) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	deadline, _ := ctx.Deadline()
	if err := file.SetWriteDeadline(deadline); err != nil {
		if !errors.Is(err, os.ErrNoDeadline) {
			return 0, err
		}
		// Regular files do not support poller deadlines. Other handles are
		// privately opened nonblocking: an unsupported device fails rather
		// than parking a worker in a blocking pipe/terminal write.
		return file.Write(data)
	}
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		defer close(done)
		_ = file.SetWriteDeadline(time.Now())
	})
	defer func() {
		if !stop() {
			<-done
		}
		_ = file.SetWriteDeadline(time.Time{})
	}()
	n, err := file.Write(data)
	if ctx.Err() != nil {
		return n, ctx.Err()
	}
	return n, err
}
