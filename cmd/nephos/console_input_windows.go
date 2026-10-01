package main

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"time"

	"golang.org/x/sys/windows"
)

var cancelConsoleRead = windows.NewLazySystemDLL("kernel32.dll").NewProc("CancelSynchronousIo")

func readConsoleFile(ctx context.Context, file *os.File, buf []byte) (int, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	thread, err := windows.GetCurrentThread()
	if err != nil {
		return 0, fmt.Errorf("get console reader thread: %w", err)
	}
	process := windows.CurrentProcess()
	var owned windows.Handle
	if err := windows.DuplicateHandle(process, thread, process, &owned, 0, false, windows.DUPLICATE_SAME_ACCESS); err != nil {
		return 0, fmt.Errorf("pin console reader thread: %w", err)
	}
	defer func() { _ = windows.CloseHandle(owned) }()
	done := make(chan struct{})
	canceled := make(chan struct{})
	inputHandle := windows.Handle(file.Fd())
	stop := context.AfterFunc(ctx, func() {
		defer close(canceled)
		ticker := time.NewTicker(25 * time.Millisecond)
		defer ticker.Stop()
		for {
			// Repeat to close the race between ctx.Err and entering ReadFile.
			_, _, _ = cancelConsoleRead.Call(uintptr(owned))
			_ = windows.CancelIoEx(inputHandle, nil)
			select {
			case <-done:
				return
			case <-ticker.C:
			}
		}
	})
	defer func() {
		close(done)
		if !stop() {
			<-canceled
		}
	}()
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	n, err := file.Read(buf)
	if ctx.Err() != nil {
		return n, ctx.Err()
	}
	return n, err
}
