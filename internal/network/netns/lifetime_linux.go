package netns

import (
	"context"
	"fmt"
	"os"
	"strconv"

	"golang.org/x/sys/unix"

	"github.com/Amrzxk/nephos/internal/lifecycle"
)

func trackReference(ctx context.Context, kind, identity string) func() {
	if r := lifecycle.ContextRegistry(ctx); r != nil {
		return r.Track("default", kind, identity, nil)
	}
	return func() {}
}

func rememberNamespace(ctx context.Context, inode uint64) {
	if r := lifecycle.ContextRegistry(ctx); r != nil {
		r.Remember("default", "namespace", strconv.FormatUint(inode, 10))
	}
}

func reportCleanup(ctx context.Context, err error) {
	if r := lifecycle.ContextRegistry(ctx); r != nil {
		r.ReportError("default", err)
	}
}

func closeTrackedFD(ctx context.Context, fd int, release func()) error {
	if err := unix.Close(fd); err != nil {
		err = fmt.Errorf("close registered namespace FD %d: %w", fd, err)
		reportCleanup(ctx, err)
		return err
	}
	release()
	return nil
}

func closeTrackedFile(ctx context.Context, file *os.File, release func()) error {
	if err := file.Close(); err != nil {
		err = fmt.Errorf("close registered namespace file: %w", err)
		reportCleanup(ctx, err)
		return err
	}
	release()
	return nil
}
