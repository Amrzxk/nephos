package main

import (
	"context"
	"os"

	"golang.org/x/sys/windows"
)

func openConsoleOutput(file *os.File) (*os.File, func() error, error) {
	process := windows.CurrentProcess()
	var duplicate windows.Handle
	if err := windows.DuplicateHandle(process, windows.Handle(file.Fd()), process, &duplicate, 0, false, windows.DUPLICATE_SAME_ACCESS); err != nil {
		return nil, nil, err
	}
	owned := os.NewFile(uintptr(duplicate), file.Name())
	return owned, owned.Close, nil
}

func writeConsoleFile(ctx context.Context, file *os.File, data []byte) (int, error) {
	return consoleFileIO(ctx, file, func() (int, error) { return file.Write(data) })
}
