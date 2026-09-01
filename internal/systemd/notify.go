// Package systemd speaks the sd_notify protocol, so the service can tell
// systemd it has started and prove it is still alive.
//
// The protocol is a newline-separated key=value datagram sent to the socket
// named by $NOTIFY_SOCKET. That is small enough to implement directly rather
// than take on a dependency for it.
//
// Every function is a no-op when the process was not started by systemd
// (the environment variables are absent), so the same binary runs fine from a
// shell.
package systemd

import (
	"fmt"
	"log/slog"
	"net"
	"os"
	"strconv"
	"sync"
	"time"
)

// Notifier sends readiness and liveness notifications to systemd.
type Notifier struct {
	mu   sync.Mutex
	conn *net.UnixConn

	// watchdogInterval is how often WATCHDOG=1 must be sent, derived from
	// WATCHDOG_USEC. Zero means systemd is not watching.
	watchdogInterval time.Duration
}

// Connect dials the notification socket. It returns a usable no-op Notifier
// when the process is not running under systemd.
func Connect() *Notifier {
	n := &Notifier{}

	socketPath := os.Getenv("NOTIFY_SOCKET")
	if socketPath == "" {
		return n
	}
	// An abstract socket is written with a leading NUL rather than '@'.
	if socketPath[0] == '@' {
		socketPath = "\x00" + socketPath[1:]
	}

	conn, err := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: socketPath, Net: "unixgram"})
	if err != nil {
		slog.Warn("could not connect to the systemd notify socket", "error", err)
		return n
	}
	n.conn = conn
	n.watchdogInterval = watchdogInterval()
	return n
}

// watchdogInterval reads the ping interval systemd expects. WATCHDOG_PID
// guards against the variables being inherited by a child process.
func watchdogInterval() time.Duration {
	raw := os.Getenv("WATCHDOG_USEC")
	if raw == "" {
		return 0
	}
	if pid := os.Getenv("WATCHDOG_PID"); pid != "" && pid != strconv.Itoa(os.Getpid()) {
		return 0
	}
	usec, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || usec <= 0 {
		return 0
	}
	// systemd's own guidance is to ping at half the configured interval, so a
	// single slow cycle does not trip the watchdog.
	return time.Duration(usec) * time.Microsecond / 2
}

// WatchdogEnabled reports whether systemd is watching for liveness pings.
func (n *Notifier) WatchdogEnabled() bool { return n.watchdogInterval > 0 }

// WatchdogInterval is how often Alive should be called.
func (n *Notifier) WatchdogInterval() time.Duration { return n.watchdogInterval }

// Ready tells systemd that startup is complete. Type=notify units stay in
// "activating" until this arrives.
func (n *Notifier) Ready() { n.send("READY=1") }

// Alive sends a liveness ping. Withholding it is how a wedged process gets
// itself restarted, so it must only be called from a path that would stop
// running if the service hung.
func (n *Notifier) Alive() { n.send("WATCHDOG=1") }

// Stopping tells systemd the shutdown is deliberate.
func (n *Notifier) Stopping() { n.send("STOPPING=1") }

// Status sets the one-line status shown by `systemctl status`.
func (n *Notifier) Status(format string, args ...any) {
	n.send("STATUS=" + fmt.Sprintf(format, args...))
}

// Close releases the socket.
func (n *Notifier) Close() error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.conn == nil {
		return nil
	}
	err := n.conn.Close()
	n.conn = nil
	return err
}

func (n *Notifier) send(state string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.conn == nil {
		return
	}
	if _, err := n.conn.Write([]byte(state + "\n")); err != nil {
		slog.Debug("systemd notification failed", "state", state, "error", err)
	}
}
