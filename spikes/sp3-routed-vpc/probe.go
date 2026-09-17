package main

// Opening real sockets inside network namespaces.
//
// This proves ADR-0005 validation 5 (Go can open DNS and metadata listeners
// inside a VPC namespace) and provides the traffic generator the connectivity
// assertions need. It is the prototype of internal/probe.
//
// The listener case is the subtle one. A socket belongs to the namespace its
// creating THREAD was in, but it keeps working afterwards from any thread. So
// the socket is created on a locked, soon-to-be-discarded thread, and only the
// accept loop runs on ordinary goroutines. That is what lets nephosd serve
// per-VPC DNS and IMDS from one process without stranding threads in other
// namespaces (RISKS T8).

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"syscall"
	"time"
)

// ProbeResult classifies an attempt the way ARCHITECTURE §8.1 does. The
// distinction that matters for learning: a dropped packet times out, while a
// closed port is refused. A learner must be able to tell "the firewall blocked
// me" from "nothing is listening".
type ProbeResult string

const (
	Reachable   ProbeResult = "reachable"
	Refused     ProbeResult = "refused"
	Timeout     ProbeResult = "timeout"
	Unreachable ProbeResult = "unreachable"
	ProbeError  ProbeResult = "error"
)

// DialFrom attempts a TCP connection from inside a namespace.
func DialFrom(nsName, addr string, timeout time.Duration) (ProbeResult, error) {
	var result ProbeResult
	err := Do(nsName, func() error {
		conn, err := net.DialTimeout("tcp", addr, timeout)
		if err == nil {
			_ = conn.Close()
			result = Reachable
			return nil
		}
		result = classify(err)
		return nil
	})
	if err != nil {
		return ProbeError, err
	}
	return result, nil
}

func classify(err error) ProbeResult {
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return Timeout
	}
	switch {
	case errors.Is(err, syscall.ECONNREFUSED):
		return Refused
	case errors.Is(err, syscall.EHOSTUNREACH), errors.Is(err, syscall.ENETUNREACH):
		return Unreachable
	}
	return ProbeError
}

// Listener is a TCP listener whose socket was created inside a namespace.
type Listener struct {
	ln net.Listener
	ns string
}

// ListenIn opens a TCP listener inside a namespace and serves a fixed banner
// to anything that connects.
//
// The socket is created on the namespace-switched thread; the accept loop then
// runs on ordinary goroutines, because the socket has already been bound and
// carries its namespace with it.
func ListenIn(nsName, addr, banner string) (*Listener, error) {
	type res struct {
		ln  net.Listener
		err error
	}
	out := make(chan res, 1)

	if err := Do(nsName, func() error {
		ln, err := net.Listen("tcp", addr)
		out <- res{ln, err}
		return err
	}); err != nil {
		return nil, fmt.Errorf("listening on %s in %s: %w", addr, nsName, err)
	}

	r := <-out
	if r.err != nil {
		return nil, fmt.Errorf("listening on %s in %s: %w", addr, nsName, r.err)
	}

	l := &Listener{ln: r.ln, ns: nsName}
	go func() {
		for {
			conn, err := l.ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				_ = c.SetDeadline(time.Now().Add(5 * time.Second))
				_, _ = io.WriteString(c, banner)
			}(conn)
		}
	}()
	return l, nil
}

// dialWithTimeout is a plain TCP dial. The caller must already be inside the
// namespace it wants the connection to originate from.
func dialWithTimeout(addr string, timeout time.Duration) (net.Conn, error) {
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return nil, fmt.Errorf("dialling %s: %w", addr, err)
	}
	return conn, nil
}

// Addr reports the address the listener bound to.
func (l *Listener) Addr() string { return l.ln.Addr().String() }

// Close stops the listener.
func (l *Listener) Close() error {
	if l == nil || l.ln == nil {
		return nil
	}
	return l.ln.Close()
}

// ListenUDPIn opens a UDP socket inside a namespace, which is what the per-VPC
// DNS resolver will need (ARCHITECTURE §5.1).
func ListenUDPIn(nsName, addr string) (*net.UDPConn, error) {
	type res struct {
		conn *net.UDPConn
		err  error
	}
	out := make(chan res, 1)

	if err := Do(nsName, func() error {
		ua, err := net.ResolveUDPAddr("udp", addr)
		if err != nil {
			out <- res{nil, err}
			return err
		}
		conn, err := net.ListenUDP("udp", ua)
		out <- res{conn, err}
		return err
	}); err != nil {
		return nil, fmt.Errorf("listening on udp %s in %s: %w", addr, nsName, err)
	}
	r := <-out
	if r.err != nil {
		return nil, fmt.Errorf("listening on udp %s in %s: %w", addr, nsName, r.err)
	}
	return r.conn, nil
}

// ContinuousProbe repeatedly dials an address from a namespace and counts what
// happened. ADR-0005 validation 4 uses it: while the ruleset is replaced 100
// times, a flow that must be denied must never once succeed.
//
// A denied flow costs the whole dial timeout, so a single prober with a long
// timeout manages only a handful of attempts and proves very little. Several
// probers with a short timeout sample the replacement window densely enough for
// "never allowed" to mean something.
type ContinuousProbe struct {
	mu       sync.Mutex
	Attempts int
	Allowed  int
	Denied   int
	Errors   int
	stop     chan struct{}
	wg       sync.WaitGroup
}

// StartContinuousProbe dials in a tight loop from `workers` goroutines until
// Stop is called.
func StartContinuousProbe(nsName, addr string, interval time.Duration, workers int) *ContinuousProbe {
	if workers < 1 {
		workers = 1
	}
	p := &ContinuousProbe{stop: make(chan struct{})}
	for i := 0; i < workers; i++ {
		p.wg.Add(1)
		go func() {
			defer p.wg.Done()
			for {
				select {
				case <-p.stop:
					return
				default:
				}
				result, err := DialFrom(nsName, addr, 100*time.Millisecond)
				p.mu.Lock()
				p.Attempts++
				switch {
				case err != nil:
					p.Errors++
				case result == Reachable:
					p.Allowed++
				default:
					p.Denied++
				}
				p.mu.Unlock()
				time.Sleep(interval)
			}
		}()
	}
	return p
}

// Stop ends the probe and waits for its goroutines to finish.
func (p *ContinuousProbe) Stop() {
	close(p.stop)
	p.wg.Wait()
}

// HostHasNftables reports whether nft is usable here, so a spike can fail with
// a clear message instead of a confusing one.
func HostHasNftables() error {
	if _, err := os.Stat("/proc/net/netfilter"); err != nil {
		return fmt.Errorf("netfilter does not appear to be available: %w", err)
	}
	return nil
}
