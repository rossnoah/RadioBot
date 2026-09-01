package radio

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// The supervisor keeps dsd-fme running, and nothing more. When it cannot, it
// says so and hands the problem to the escalation policy, which decides
// between restarting the service (via systemd) and giving up gracefully.
//
// This is deliberately the whole of its responsibility. The previous design
// also owned reboots, which meant a fault that survived a power cycle — a
// wedged USB controller, a bad config, an unplugged antenna — produced an
// endless reboot loop with no memory of having tried.
// maxRestartsBeforeEscalation is how many consecutive short-lived runs to
// tolerate before handing the problem upwards.
const maxRestartsBeforeEscalation = 5

// Supervisor timings. They are variables rather than constants so tests can
// drive the whole loop in milliseconds instead of minutes.
var (
	// checkInterval is how often a running process is inspected. It is also
	// the fastest rate at which the supervisor records a heartbeat.
	checkInterval = 15 * time.Second

	// Restart backoff. A process that stays up for healthyRun resets both the
	// backoff and the failure count.
	initialBackoff = 2 * time.Second
	maxBackoff     = 60 * time.Second
	healthyRun     = 5 * time.Minute

	// degradedBackoff is the retry interval once escalation has been
	// abandoned: keep trying, but slowly, in case someone fixes the hardware.
	degradedBackoff = 5 * time.Minute

	// startSettleDelay is how long to wait after spawning dsd-fme before
	// trusting that it started, so an immediate failure is reported as one.
	startSettleDelay = time.Second
)

// HeartbeatInterval is how often Supervise refreshes its heartbeat while it is
// healthy. Anything watching for a wedged supervisor should allow several of
// these to pass before acting.
func HeartbeatInterval() time.Duration { return checkInterval }

// Escalator decides what happens when the radio cannot be recovered here.
type Escalator interface {
	// Escalate reports an unrecoverable radio. It returns true if the
	// supervisor should continue in degraded mode, false if the process is
	// being shut down.
	Escalate(reason string) (degraded bool)

	// Recovered reports that the radio came back on its own.
	Recovered()
}

// fault describes why a running process needs to be replaced.
type fault struct {
	reason string

	// hardware means the SDR itself is gone, so restarting dsd-fme cannot
	// help and the problem should go upwards immediately.
	hardware bool

	// planned means this is routine maintenance (a log rotation), not a
	// failure, so it must not count against the restart budget.
	planned bool
}

// Supervise runs the radio process until ctx is cancelled, restarting it with
// backoff and escalating when it cannot be kept alive. It owns the process
// lifecycle end to end — nothing else should start or stop the receiver.
func (m *Manager) Supervise(ctx context.Context, escalator Escalator) {
	defer m.stop()

	failures := 0
	backoff := initialBackoff
	degraded := false

	// onHealthy is called as soon as a run has lasted long enough to count as
	// recovery — while the process is still up, not when it next dies, so a
	// radio that comes back and stays back clears the degraded state.
	// Everything it touches belongs to this goroutine, and watch calls it from
	// this goroutine, so no locking is needed. It is idempotent.
	onHealthy := func() {
		failures = 0
		backoff = initialBackoff
		if degraded {
			degraded = false
			escalator.Recovered()
		}
	}

	for ctx.Err() == nil {
		f, ranFor := m.runOnce(ctx, onHealthy)
		if ctx.Err() != nil {
			return
		}

		if f.planned {
			slog.Info("restarting the radio process", "reason", f.reason)
			continue
		}

		failures++
		slog.Warn("radio process needs restarting",
			"reason", f.reason, "ran_for", ranFor.Round(time.Second),
			"consecutive_failures", failures)

		if !degraded && (f.hardware || failures >= maxRestartsBeforeEscalation) {
			if degraded = escalator.Escalate(escalationReason(f, failures)); !degraded {
				return // the process is shutting down
			}
			failures = 0
		}

		wait := backoff
		if degraded {
			wait = degradedBackoff
		} else {
			backoff = min(backoff*2, maxBackoff)
		}
		if !m.wait(ctx, wait) {
			return
		}
	}
}

// escalationReason phrases the fault for the alert an operator will read.
func escalationReason(f fault, failures int) string {
	if f.hardware {
		return f.reason
	}
	return fmt.Sprintf("%s (%d consecutive failed runs)", f.reason, failures)
}

// runOnce starts the radio and watches it until it needs replacing, returning
// the fault and how long the process lasted. onHealthy is called once the run
// has lasted long enough to count as a recovery.
func (m *Manager) runOnce(ctx context.Context, onHealthy func()) (fault, time.Duration) {
	startedAt := time.Now()

	if err := m.start(); err != nil {
		m.beat() // the supervisor is alive even when the radio is not
		return fault{reason: fmt.Sprintf("could not start: %v", err)}, 0
	}

	f := m.watch(ctx, onHealthy)
	ranFor := time.Since(startedAt)
	if ranFor >= healthyRun {
		onHealthy() // covers a run that ended between health checks
	}
	m.stop()

	if f.reason != "" && !f.planned {
		m.logRestart(f.reason, ranFor)
	}
	return f, ranFor
}

// watch inspects a running process until it exits, wedges, or ctx is
// cancelled. A zero-value fault means the context was cancelled.
func (m *Manager) watch(ctx context.Context, onHealthy func()) fault {
	m.mu.Lock()
	exited := m.exited
	m.mu.Unlock()

	startedAt := time.Now()
	reportedHealthy := false

	ticker := time.NewTicker(checkInterval)
	defer ticker.Stop()

	m.beat()
	for {
		select {
		case <-ctx.Done():
			return fault{}
		case <-exited:
			return fault{reason: "process exited"}
		case <-ticker.C:
			m.beat()
			if !reportedHealthy && time.Since(startedAt) >= healthyRun {
				reportedHealthy = true
				onHealthy()
			}
			if f, unhealthy := m.checkHealth(); unhealthy {
				return f
			}
		}
	}
}

// checkHealth runs the periodic inspections of a live process.
func (m *Manager) checkHealth() (fault, bool) {
	// The device disappearing from the USB bus is the one fault a restart
	// cannot fix, so it is checked first and reported as hardware.
	if !rtlSDRPresent() {
		return fault{
			reason:   "the RTL-SDR is no longer on the USB bus",
			hardware: true,
		}, true
	}

	// dsd-fme holds the log file open, so rotating it requires a restart.
	if m.logNeedsRotation() {
		if err := m.rotateLog(); err != nil {
			slog.Error("log rotation failed", "error", err)
			return fault{}, false
		}
		return fault{reason: "log rotation", planned: true}, true
	}

	if idle, frozen := m.idleFor(); frozen {
		return fault{
			reason: fmt.Sprintf("no output for %s", idle.Round(time.Second)),
		}, true
	}

	return fault{}, false
}

// idleFor reports how long dsd-fme has gone without writing to its log, and
// whether that exceeds the configured timeout.
//
// This is a proxy for liveness, not for reception: it shows the process is
// still running its loop, not that RF is reaching it. How useful it is
// depends on what dsd-fme emits on a quiet channel, which is why the timeout
// is configurable and can be switched off entirely.
func (m *Manager) idleFor() (time.Duration, bool) {
	if m.frozenTimeout <= 0 {
		return 0, false
	}
	info, err := os.Stat(LogFile)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Error("could not check the radio log", "error", err)
		}
		return 0, false
	}
	idle := time.Since(info.ModTime())
	return idle, idle > m.frozenTimeout
}

// wait sleeps for d, continuing to beat so a long backoff does not look like
// a hung supervisor. It reports false if ctx was cancelled first.
func (m *Manager) wait(ctx context.Context, d time.Duration) bool {
	deadline := time.After(d)
	ticker := time.NewTicker(checkInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return false
		case <-deadline:
			return true
		case <-ticker.C:
			m.beat()
		}
	}
}

// beat records that the supervisor completed a cycle.
func (m *Manager) beat() {
	m.mu.Lock()
	m.lastBeat = time.Now()
	m.mu.Unlock()
}

// Heartbeat is when the supervisor last completed a health check. The systemd
// watchdog ping is gated on this, so a wedged supervisor stops the pings and
// systemd restarts the service.
func (m *Manager) Heartbeat() time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastBeat
}

// logRestart records why the radio was restarted and how long it had been up.
func (m *Manager) logRestart(reason string, ranFor time.Duration) {
	uptime := int(ranFor.Seconds())
	if err := m.store.LogRestart(reason, &uptime); err != nil {
		slog.Error("could not log restart", "reason", reason, "error", err)
	}
}

// logNeedsRotation reports whether the radio log has outgrown its limit.
func (m *Manager) logNeedsRotation() bool {
	info, err := os.Stat(LogFile)
	return err == nil && info.Size() >= logMaxSize
}

// rotateLog shifts dsd-fme.jsonl down the numbered backups. The caller must
// restart dsd-fme afterwards, since it holds the old file open.
func (m *Manager) rotateLog() error {
	info, err := os.Stat(LogFile)
	if err != nil {
		return err
	}
	slog.Info("rotating the radio log",
		"size_mb", info.Size()/1024/1024, "limit_mb", int64(logMaxSize)/1024/1024)

	oldest := fmt.Sprintf("%s.%d", LogFile, logBackupCount)
	if err := os.Remove(oldest); err != nil && !os.IsNotExist(err) {
		slog.Warn("could not remove the oldest log backup", "path", oldest, "error", err)
	}
	for i := logBackupCount - 1; i >= 1; i-- {
		src := fmt.Sprintf("%s.%d", LogFile, i)
		dst := fmt.Sprintf("%s.%d", LogFile, i+1)
		if err := os.Rename(src, dst); err != nil && !os.IsNotExist(err) {
			slog.Warn("could not rotate a log backup", "from", src, "to", dst, "error", err)
		}
	}
	return os.Rename(LogFile, LogFile+".1")
}

// rtlSDRPresent reports whether an RTL-SDR is visible on the USB bus. When the
// check cannot run — no sysfs, so not Linux — it assumes the device is there,
// so an unrelated environment never triggers an escalation.
func rtlSDRPresent() bool {
	const usbDevices = "/sys/bus/usb/devices"
	entries, err := os.ReadDir(usbDevices)
	if err != nil {
		return true
	}
	for _, entry := range entries {
		vendor, err := os.ReadFile(filepath.Join(usbDevices, entry.Name(), "idVendor"))
		if err != nil {
			continue
		}
		if strings.TrimSpace(string(vendor)) == rtlSDRUSBVendorID {
			return true
		}
	}
	return false
}
