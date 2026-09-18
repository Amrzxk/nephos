// Command plumbd is the spike stand-in for nephosd's hook endpoint.
//
// It listens on /run/nephos/hook.sock and, when the OCI hook calls it, wires an
// instance's eth0 into the target container's network namespace BEFORE PID 1
// runs (ADR-0004 R3, ADR-0005).
//
// The property under test is ownership, not just connectivity. The instance's
// network namespace must belong to the instance's USER namespace, so that
// instance root holds CAP_NET_ADMIN in it and can run ufw — while the
// enforcement Nephos cares about stays outside, on the router side of the veth,
// where the learner cannot reach it.
package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sync"

	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
	"golang.org/x/sys/unix"
)

const socketPath = "/run/nephos/hook.sock"

// plumbRequest is what the hook sends.
type plumbRequest struct {
	InstanceID  string `json:"instance_id"`
	Pid         int    `json:"pid"`
	ContainerID string `json:"container_id"`
}

// eniSpec is the desired state for one interface. In the real system this comes
// from the database; here it comes from a small in-memory registry the spike
// populates before starting each container.
type eniSpec struct {
	Iface     string // router-side veth name, at most 15 characters
	PrivateIP string
	PrefixLen int
	Gateway   string
	MTU       int
	RouterNS  string // the VPC namespace holding the router end
}

var (
	registryMu sync.Mutex
	registry   = map[string]eniSpec{}
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	slog.SetDefault(logger)

	if len(os.Args) > 2 && os.Args[1] == "register" {
		// plumbd register '<json>' — used by run.sh before starting a container.
		if err := registerFromArg(os.Args[2]); err != nil {
			logger.Error("registration failed", slog.Any("error", err))
			os.Exit(1)
		}
		return
	}

	if err := serve(logger); err != nil {
		logger.Error("plumbd exited", slog.Any("error", err))
		os.Exit(1)
	}
}

func registerFromArg(arg string) error {
	var entry struct {
		InstanceID string  `json:"instance_id"`
		Spec       eniSpec `json:"spec"`
	}
	if err := json.Unmarshal([]byte(arg), &entry); err != nil {
		return fmt.Errorf("parsing the registration: %w", err)
	}
	// The registry is shared with the server through a file so that `register`
	// can run as a separate process.
	return appendRegistration(entry.InstanceID, entry.Spec)
}

const registryPath = "/run/nephos/registry.json"

func appendRegistration(id string, spec eniSpec) error {
	all := map[string]eniSpec{}
	if data, err := os.ReadFile(registryPath); err == nil {
		_ = json.Unmarshal(data, &all)
	}
	all[id] = spec
	data, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding the registry: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(registryPath), 0o755); err != nil {
		return fmt.Errorf("creating the registry directory: %w", err)
	}
	if err := os.WriteFile(registryPath, data, 0o644); err != nil {
		return fmt.Errorf("writing the registry: %w", err)
	}
	return nil
}

func lookup(id string) (eniSpec, bool) {
	registryMu.Lock()
	defer registryMu.Unlock()
	if spec, ok := registry[id]; ok {
		return spec, true
	}
	all := map[string]eniSpec{}
	data, err := os.ReadFile(registryPath)
	if err != nil {
		return eniSpec{}, false
	}
	if err := json.Unmarshal(data, &all); err != nil {
		return eniSpec{}, false
	}
	spec, ok := all[id]
	return spec, ok
}

func serve(logger *slog.Logger) error {
	if err := os.MkdirAll(filepath.Dir(socketPath), 0o755); err != nil {
		return fmt.Errorf("creating the socket directory: %w", err)
	}
	_ = os.Remove(socketPath)

	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", socketPath, err)
	}
	defer ln.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("/plumb", func(w http.ResponseWriter, r *http.Request) {
		var req plumbRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, fmt.Sprintf("bad request: %v", err), http.StatusBadRequest)
			return
		}
		log := logger.With(slog.String("instance_id", req.InstanceID), slog.Int("pid", req.Pid))

		spec, ok := lookup(req.InstanceID)
		if !ok {
			// Fail closed: an instance Nephos does not recognise must not boot
			// with an unconfigured network.
			log.Error("no ENI registered for this instance")
			http.Error(w, "no ENI registered for "+req.InstanceID, http.StatusNotFound)
			return
		}

		if err := plumb(req.Pid, spec); err != nil {
			log.Error("plumbing failed", slog.Any("error", err))
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		log.Info("instance plumbed", slog.String("iface", spec.Iface), slog.String("ip", spec.PrivateIP))
		w.WriteHeader(http.StatusOK)
	})

	logger.Info("plumbd listening", slog.String("socket", socketPath))
	server := &http.Server{Handler: mux}
	return server.Serve(ln)
}

// plumb creates the veth pair, moves the router end into the VPC namespace and
// the instance end into the container's namespace, and configures both.
//
// The container's network namespace is reached through /proc/<pid>/ns/net,
// which is the only handle available at createRuntime time: the container is
// not yet running, so there is nothing to exec into.
func plumb(pid int, spec eniSpec) error {
	instSide := spec.Iface + "p"

	// Remove any leftover from a previous attempt so the operation is
	// idempotent, as every engine operation must be (ADR-0007).
	if link, err := netlink.LinkByName(spec.Iface); err == nil {
		_ = netlink.LinkDel(link)
	}

	veth := &netlink.Veth{
		LinkAttrs: netlink.LinkAttrs{Name: spec.Iface, MTU: spec.MTU},
		PeerName:  instSide,
	}
	if err := netlink.LinkAdd(veth); err != nil {
		return fmt.Errorf("creating veth %s: %w", spec.Iface, err)
	}

	// Router end into the VPC namespace, with proxy ARP and a /32 route: there
	// is no layer-2 domain, so the router answers ARP and forwards by host route.
	if err := moveToNamedNS(spec.Iface, spec.RouterNS); err != nil {
		return err
	}
	if err := inNamedNS(spec.RouterNS, func() error {
		link, err := netlink.LinkByName(spec.Iface)
		if err != nil {
			return fmt.Errorf("finding %s in %s: %w", spec.Iface, spec.RouterNS, err)
		}
		if err := netlink.LinkSetUp(link); err != nil {
			return fmt.Errorf("bringing up %s: %w", spec.Iface, err)
		}
		for key, value := range map[string]string{
			"proxy_arp":      "1",
			"send_redirects": "0",
		} {
			path := fmt.Sprintf("/proc/sys/net/ipv4/conf/%s/%s", spec.Iface, key)
			if err := os.WriteFile(path, []byte(value), 0o644); err != nil {
				return fmt.Errorf("setting %s on %s: %w", key, spec.Iface, err)
			}
		}
		_, dst, err := net.ParseCIDR(spec.PrivateIP + "/32")
		if err != nil {
			return fmt.Errorf("parsing the instance address: %w", err)
		}
		route := &netlink.Route{LinkIndex: link.Attrs().Index, Dst: dst, Scope: netlink.SCOPE_LINK}
		if err := netlink.RouteAdd(route); err != nil && !os.IsExist(err) {
			return fmt.Errorf("adding the /32 route to %s: %w", spec.PrivateIP, err)
		}
		return nil
	}); err != nil {
		return err
	}

	// Instance end into the container's namespace, by PID.
	return moveToPidNSAndConfigure(instSide, pid, spec)
}

func moveToNamedNS(iface, nsName string) error {
	link, err := netlink.LinkByName(iface)
	if err != nil {
		return fmt.Errorf("finding %s: %w", iface, err)
	}
	handle, err := netns.GetFromPath("/run/netns/" + nsName)
	if err != nil {
		return fmt.Errorf("opening namespace %s: %w", nsName, err)
	}
	defer handle.Close()
	if err := netlink.LinkSetNsFd(link, int(handle)); err != nil {
		return fmt.Errorf("moving %s into %s: %w", iface, nsName, err)
	}
	return nil
}

func moveToPidNSAndConfigure(iface string, pid int, spec eniSpec) error {
	link, err := netlink.LinkByName(iface)
	if err != nil {
		return fmt.Errorf("finding %s: %w", iface, err)
	}
	if err := netlink.LinkSetNsPid(link, pid); err != nil {
		return fmt.Errorf("moving %s into the namespace of pid %d: %w", iface, pid, err)
	}

	return inPidNS(pid, func() error {
		link, err := netlink.LinkByName(iface)
		if err != nil {
			return fmt.Errorf("finding %s inside the instance: %w", iface, err)
		}
		// The instance always sees its interface as eth0, whatever the router
		// side is called.
		if err := netlink.LinkSetName(link, "eth0"); err != nil {
			return fmt.Errorf("renaming %s to eth0: %w", iface, err)
		}
		link, err = netlink.LinkByName("eth0")
		if err != nil {
			return fmt.Errorf("finding eth0: %w", err)
		}
		if err := netlink.LinkSetUp(link); err != nil {
			return fmt.Errorf("bringing up eth0: %w", err)
		}
		if lo, err := netlink.LinkByName("lo"); err == nil {
			_ = netlink.LinkSetUp(lo)
		}

		addr, err := netlink.ParseAddr(fmt.Sprintf("%s/%d", spec.PrivateIP, spec.PrefixLen))
		if err != nil {
			return fmt.Errorf("parsing the instance address: %w", err)
		}
		if err := netlink.AddrAdd(link, addr); err != nil && !os.IsExist(err) {
			return fmt.Errorf("adding %s to eth0: %w", addr, err)
		}

		// The gateway is on the router's dummy interface, outside the
		// instance's own prefix, so it needs an explicit on-link route first.
		_, gwNet, err := net.ParseCIDR(spec.Gateway + "/32")
		if err != nil {
			return fmt.Errorf("parsing the gateway: %w", err)
		}
		if err := netlink.RouteAdd(&netlink.Route{
			LinkIndex: link.Attrs().Index, Dst: gwNet, Scope: netlink.SCOPE_LINK,
		}); err != nil && !os.IsExist(err) {
			return fmt.Errorf("adding the on-link route to the gateway: %w", err)
		}
		if err := netlink.RouteAdd(&netlink.Route{
			LinkIndex: link.Attrs().Index, Gw: net.ParseIP(spec.Gateway),
		}); err != nil && !os.IsExist(err) {
			return fmt.Errorf("adding the default route: %w", err)
		}
		return nil
	})
}

// inNamedNS and inPidNS both run fn on a locked thread that is never returned
// to the scheduler (RISKS T8). See ADR-0002: a thread left in another namespace
// would later create objects in the wrong place, intermittently.
func inNamedNS(name string, fn func() error) error {
	handle, err := netns.GetFromPath("/run/netns/" + name)
	if err != nil {
		return fmt.Errorf("opening namespace %s: %w", name, err)
	}
	defer handle.Close()
	return inNS(handle, fn)
}

func inPidNS(pid int, fn func() error) error {
	handle, err := netns.GetFromPath(fmt.Sprintf("/proc/%d/ns/net", pid))
	if err != nil {
		return fmt.Errorf("opening the namespace of pid %d: %w", pid, err)
	}
	defer handle.Close()
	return inNS(handle, fn)
}

func inNS(target netns.NsHandle, fn func() error) error {
	done := make(chan error, 1)
	go func() {
		// No matching UnlockOSThread: the runtime destroys this thread when
		// the goroutine returns, which is exactly what is wanted.
		runtime.LockOSThread()

		origin, err := netns.Get()
		if err != nil {
			done <- fmt.Errorf("capturing the current namespace: %w", err)
			return
		}
		defer origin.Close()

		if err := netns.Set(target); err != nil {
			done <- fmt.Errorf("entering the target namespace: %w", err)
			return
		}
		defer func() { _ = netns.Set(origin) }()

		done <- fn()
	}()
	return <-done
}

// NamespaceOwnerUID reports the UID that owns a network namespace's user
// namespace. A non-zero answer is what proves the instance's netns belongs to
// the instance's user namespace, which is what lets instance root run ufw
// (ADR-0004 R3).
func NamespaceOwnerUID(pid int) (uint32, error) {
	f, err := os.Open(fmt.Sprintf("/proc/%d/ns/net", pid))
	if err != nil {
		return 0, fmt.Errorf("opening the namespace of pid %d: %w", pid, err)
	}
	defer f.Close()

	userNS, err := unix.IoctlRetInt(int(f.Fd()), unix.NS_GET_USERNS)
	if err != nil {
		return 0, fmt.Errorf("NS_GET_USERNS: %w", err)
	}
	defer unix.Close(userNS)

	uid, err := unix.IoctlGetUint32(userNS, unix.NS_GET_OWNER_UID)
	if err != nil {
		return 0, fmt.Errorf("NS_GET_OWNER_UID: %w", err)
	}
	return uid, nil
}
