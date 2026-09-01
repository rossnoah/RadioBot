package systemd

import (
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// listen sets up a notification socket and points NOTIFY_SOCKET at it.
func listen(t *testing.T) *net.UnixConn {
	t.Helper()

	// The socket path has to be short enough for sun_path, so avoid t.TempDir
	// on platforms with long temp paths.
	dir, err := os.MkdirTemp("", "sd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	path := filepath.Join(dir, "notify")
	conn, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: path, Net: "unixgram"})
	if err != nil {
		t.Skipf("unix datagram sockets unavailable: %v", err)
	}
	t.Cleanup(func() { conn.Close() })

	t.Setenv("NOTIFY_SOCKET", path)
	return conn
}

// receive reads one datagram.
func receive(t *testing.T, conn *net.UnixConn) string {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4096)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return string(buf[:n])
}

func TestNotificationsReachSystemd(t *testing.T) {
	conn := listen(t)

	notifier := Connect()
	defer notifier.Close()

	notifier.Ready()
	if got := receive(t, conn); got != "READY=1\n" {
		t.Errorf("Ready sent %q", got)
	}

	notifier.Alive()
	if got := receive(t, conn); got != "WATCHDOG=1\n" {
		t.Errorf("Alive sent %q", got)
	}

	notifier.Status("serving on %s", ":4000")
	if got := receive(t, conn); got != "STATUS=serving on :4000\n" {
		t.Errorf("Status sent %q", got)
	}

	notifier.Stopping()
	if got := receive(t, conn); got != "STOPPING=1\n" {
		t.Errorf("Stopping sent %q", got)
	}
}

// TestWithoutSystemdIsANoOp keeps the binary runnable from a shell.
func TestWithoutSystemdIsANoOp(t *testing.T) {
	t.Setenv("NOTIFY_SOCKET", "")
	t.Setenv("WATCHDOG_USEC", "")

	notifier := Connect()
	defer notifier.Close()

	if notifier.WatchdogEnabled() {
		t.Error("the watchdog is enabled without systemd")
	}
	// None of these should panic.
	notifier.Ready()
	notifier.Alive()
	notifier.Status("idle")
	notifier.Stopping()
}

// TestWatchdogIntervalIsHalved follows systemd's guidance of pinging at half
// the configured interval, so one slow cycle does not trip the watchdog.
func TestWatchdogIntervalIsHalved(t *testing.T) {
	listen(t)
	t.Setenv("WATCHDOG_USEC", "120000000") // 120s

	notifier := Connect()
	defer notifier.Close()

	if !notifier.WatchdogEnabled() {
		t.Fatal("the watchdog should be enabled")
	}
	if got := notifier.WatchdogInterval(); got != 60*time.Second {
		t.Errorf("interval = %v, want 60s", got)
	}
}

// TestWatchdogIgnoresInheritedEnvironment covers WATCHDOG_PID: the variables
// are inherited by children, which must not think they are being watched.
func TestWatchdogIgnoresInheritedEnvironment(t *testing.T) {
	listen(t)
	t.Setenv("WATCHDOG_USEC", "120000000")
	t.Setenv("WATCHDOG_PID", strconv.Itoa(os.Getpid()+1))

	notifier := Connect()
	defer notifier.Close()

	if notifier.WatchdogEnabled() {
		t.Error("a process claimed a watchdog meant for a different PID")
	}
}

func TestWatchdogHandlesJunkInterval(t *testing.T) {
	listen(t)
	for _, value := range []string{"not-a-number", "0", "-5"} {
		t.Setenv("WATCHDOG_USEC", value)
		notifier := Connect()
		if notifier.WatchdogEnabled() {
			t.Errorf("WATCHDOG_USEC=%q enabled the watchdog", value)
		}
		notifier.Close()
	}
}
