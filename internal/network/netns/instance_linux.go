package netns

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/vishvananda/netlink"
	vnetns "github.com/vishvananda/netns"
	"golang.org/x/sys/unix"

	"github.com/Amrzxk/nephos/internal/lifecycle"
)

// InstanceTarget pins a verified instance network namespace, never a bare PID.
// Duplicated descriptors keep an operation safe even if Close races it.
type InstanceTarget struct {
	mu        sync.Mutex
	namespace *os.File
	inode     uint64
	registry  *lifecycle.Registry
	release   func()
}

// OpenInstance proves runtime membership and namespace ownership before pinning.
// Opening the process directory once prevents PID reuse between proc reads.
func OpenInstance(ctx context.Context, pid int, runtimeID string) (target *InstanceTarget, result error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if pid <= 0 || !validRuntimeID(runtimeID) {
		return nil, fmt.Errorf("invalid instance PID or full runtime ID")
	}
	process, err := os.Open("/proc/" + strconv.Itoa(pid))
	if err != nil {
		return nil, fmt.Errorf("open instance process: %w", err)
	}
	defer func() { _ = process.Close() }()
	open := func(path string) (*os.File, error) {
		fd, err := unix.Openat(int(process.Fd()), path, unix.O_RDONLY|unix.O_CLOEXEC, 0)
		if err != nil {
			return nil, fmt.Errorf("open process %s: %w", path, err)
		}
		return os.NewFile(uintptr(fd), path), nil
	}
	read := func(path string) ([]byte, error) {
		file, err := open(path)
		if err != nil {
			return nil, err
		}
		defer func() { _ = file.Close() }()
		data, err := io.ReadAll(io.LimitReader(file, 65537))
		if err != nil {
			return nil, fmt.Errorf("read process %s: %w", path, err)
		}
		if len(data) > 65536 {
			return nil, fmt.Errorf("process %s exceeds identity limit", path)
		}
		return data, nil
	}
	namespace, err := open("ns/net")
	if err != nil {
		return nil, err
	}
	accepted := false
	releaseNamespace := trackReference(ctx, "namespace-fd", fmt.Sprintf("instance:%s/fd:%d", runtimeID, namespace.Fd()))
	defer func() {
		if !accepted || result != nil {
			result = errors.Join(result, closeTrackedFile(ctx, namespace, releaseNamespace))
			target = nil
		}
	}()
	var fs unix.Statfs_t
	if err := unix.Fstatfs(int(namespace.Fd()), &fs); err != nil {
		return nil, fmt.Errorf("inspect instance namespace filesystem: %w", err)
	}
	if fs.Type != unix.NSFS_MAGIC {
		return nil, fmt.Errorf("instance target is not a namespace")
	}
	var targetStat, rootStat, userStat, ownerStat unix.Stat_t
	if err := unix.Fstat(int(namespace.Fd()), &targetStat); err != nil {
		return nil, fmt.Errorf("inspect instance netns: %w", err)
	}
	if err := unix.Stat("/proc/thread-self/ns/net", &rootStat); err != nil {
		return nil, fmt.Errorf("inspect appliance netns: %w", err)
	}
	if targetStat.Ino == rootStat.Ino && targetStat.Dev == rootStat.Dev {
		return nil, fmt.Errorf("refusing appliance network namespace")
	}
	cgroup, err := read("cgroup")
	if err != nil {
		return nil, err
	}
	if !runtimeCgroupMatches(cgroup, runtimeID) {
		return nil, fmt.Errorf("PID %d does not belong to runtime %s", pid, runtimeID)
	}
	mapping, err := read("uid_map")
	if err != nil {
		return nil, err
	}
	if err := validateRootMapping(mapping); err != nil {
		return nil, err
	}
	user, err := open("ns/user")
	if err != nil {
		return nil, err
	}
	releaseUser := trackReference(ctx, "namespace-fd", fmt.Sprintf("instance-user:%s/fd:%d", runtimeID, user.Fd()))
	defer func() { result = errors.Join(result, closeTrackedFile(ctx, user, releaseUser)) }()
	if err := unix.Fstat(int(user.Fd()), &userStat); err != nil {
		return nil, fmt.Errorf("inspect instance user namespace: %w", err)
	}
	if err := unix.Stat("/proc/thread-self/ns/user", &rootStat); err != nil {
		return nil, fmt.Errorf("inspect appliance user namespace: %w", err)
	}
	if userStat.Ino == rootStat.Ino && userStat.Dev == rootStat.Dev {
		return nil, fmt.Errorf("instance shares appliance user namespace")
	}
	owner, err := unix.IoctlRetInt(int(namespace.Fd()), unix.NS_GET_USERNS)
	if err != nil {
		return nil, fmt.Errorf("inspect network namespace owner: %w", err)
	}
	releaseOwner := trackReference(ctx, "namespace-fd", fmt.Sprintf("namespace-owner:%s/fd:%d", runtimeID, owner))
	defer func() { result = errors.Join(result, closeTrackedFD(ctx, owner, releaseOwner)) }()
	if err := unix.Fstat(owner, &ownerStat); err != nil {
		return nil, fmt.Errorf("inspect owning user namespace: %w", err)
	}
	if ownerStat.Ino != userStat.Ino || ownerStat.Dev != userStat.Dev {
		return nil, fmt.Errorf("network namespace is not owned by instance user namespace")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	accepted = true
	rememberNamespace(ctx, targetStat.Ino)
	registry := lifecycle.ContextRegistry(ctx)
	if registry != nil {
		registry.Remember("default", "runtime", runtimeID)
	}
	return &InstanceTarget{namespace: namespace, inode: targetStat.Ino, registry: registry, release: releaseNamespace}, nil
}

func validRuntimeID(id string) bool {
	if len(id) != 64 {
		return false
	}
	for _, ch := range id {
		if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') {
			return false
		}
	}
	return true
}

func runtimeCgroupMatches(raw []byte, id string) bool {
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 1 {
		return false
	}
	path, ok := strings.CutPrefix(lines[0], "0::/")
	if !ok {
		return false
	}
	for _, part := range strings.Split(path, "/") {
		if part == "libpod-"+id || part == "libpod-"+id+".scope" {
			return true
		}
	}
	return false
}

func validateRootMapping(raw []byte) error {
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) != 3 {
			return fmt.Errorf("invalid instance UID mapping")
		}
		inside, err := strconv.ParseUint(fields[0], 10, 32)
		if err != nil {
			return fmt.Errorf("decode mapped UID: %w", err)
		}
		outside, err := strconv.ParseUint(fields[1], 10, 32)
		if err != nil {
			return fmt.Errorf("decode appliance UID: %w", err)
		}
		count, err := strconv.ParseUint(fields[2], 10, 32)
		if err != nil {
			return fmt.Errorf("decode UID range: %w", err)
		}
		if count == 0 || outside+count > 1<<32 || inside+count > 1<<32 {
			return fmt.Errorf("invalid instance UID range")
		}
		if inside == 0 {
			if outside == 0 {
				return fmt.Errorf("instance root maps to appliance root")
			}
			return nil
		}
	}
	return fmt.Errorf("instance root has no nonzero appliance UID mapping")
}

// Close releases this target; already duplicated descriptors remain valid.
func (t *InstanceTarget) Close() error {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.namespace == nil {
		return nil
	}
	err := t.namespace.Close()
	t.namespace = nil
	if err != nil {
		if t.registry != nil {
			t.registry.ReportError("default", err)
		}
		return fmt.Errorf("close instance namespace: %w", err)
	}
	if t.release != nil {
		t.release()
	}
	return nil
}

// Inode identifies the pinned namespace for kernel link ownership markers.
func (t *InstanceTarget) Inode() uint64 {
	if t == nil {
		return 0
	}
	return t.inode
}

// WithFD exposes a temporary duplicate solely for a namespace-bound operation.
func (t *InstanceTarget) WithFD(ctx context.Context, fn func(int) error) (result error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	if t == nil {
		return fmt.Errorf("missing instance namespace target")
	}
	t.mu.Lock()
	if t.namespace == nil {
		t.mu.Unlock()
		return fmt.Errorf("instance namespace target is closed")
	}
	fd, err := unix.FcntlInt(t.namespace.Fd(), unix.F_DUPFD_CLOEXEC, 0)
	t.mu.Unlock()
	if err != nil {
		return fmt.Errorf("duplicate instance namespace: %w", err)
	}
	if t.registry != nil {
		ctx = lifecycle.WithRegistry(ctx, t.registry)
	}
	release := trackReference(ctx, "namespace-duplicate", fmt.Sprintf("inode:%d/fd:%d", t.inode, fd))
	defer func() { result = errors.Join(result, closeTrackedFD(ctx, fd, release)) }()
	return fn(fd)
}

// WithInstanceHandle runs on a disposable thread in the already-pinned target.
func WithInstanceHandle(ctx context.Context, target *InstanceTarget, fn func(*netlink.Handle) error) error {
	if target != nil && target.registry != nil {
		ctx = lifecycle.WithRegistry(ctx, target.registry)
	}
	return target.WithFD(ctx, func(fd int) error {
		return withPinnedHandle(ctx, vnetns.NsHandle(fd), fn)
	})
}
