package radio

import (
	"fmt"
	"log/slog"
	"os"
	"time"
)

// The watchdog keeps the receiver alive without supervision. It handles three
// distinct failures, each of which happened in practice on the Raspberry Pi:
// dsd-fme crashing, dsd-fme wedging with the process still alive, and the
// USB controller dying (recoverable only by a reboot).

func (m *Manager) startWatchdog() {
	m.mu.Lock()
	if m.watchdogRunning {
		m.mu.Unlock()
		return
	}
	m.watchdogRunning = true
	stop := make(chan struct{})
	done := make(chan struct{})
	m.watchdogStop = stop
	m.watchdogDone = done
	m.mu.Unlock()

	go m.watchdogLoop(stop, done)
	slog.Info("radio watchdog started")
}

func (m *Manager) stopWatchdog() {
	m.mu.Lock()
	if !m.watchdogRunning {
		m.mu.Unlock()
		return
	}
	m.watchdogRunning = false
	stop, done := m.watchdogStop, m.watchdogDone
	m.watchdogStop, m.watchdogDone = nil, nil
	m.mu.Unlock()

	close(stop)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		slog.Warn("radio watchdog did not stop within 10s")
	}
	slog.Info("radio watchdog stopped")
}

func (m *Manager) watchdogLoop(stop <-chan struct{}, done chan<- struct{}) {
	defer close(done)
	slog.Info("watchdog active", "frozen_after", frozenTimeout)

	ticker := time.NewTicker(frozenCheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
		}

		// A reboot ends the loop; so does an unexpected panic being avoided by
		// simply logging and continuing on the next tick.
		if m.watchdogTick() {
			return
		}
	}
}

// watchdogTick performs one health check. It returns true when the watchdog
// should stop (a reboot has been requested).
func (m *Manager) watchdogTick() bool {
	m.mu.Lock()
	started := !m.lastStart.IsZero()
	m.mu.Unlock()

	running := m.IsRunning()

	switch {
	case !running && started:
		return m.handleCrash()
	case !running:
		return m.handleStuckStopped()
	}

	// Running normally: clear the stopped timer, then check the log.
	m.mu.Lock()
	m.stoppedSince = time.Time{}
	m.mu.Unlock()

	if m.rotateLogIfNeeded() {
		return false // rotation triggered a restart; re-evaluate next tick
	}
	if m.isProcessFrozen() {
		return m.handleFrozen()
	}
	return false
}

// handleCrash responds to the process exiting on its own.
func (m *Manager) handleCrash() bool {
	slog.Warn("radio process died unexpectedly")
	m.mu.Lock()
	m.stoppedSince = time.Time{} // crash handling takes over from the stopped timer
	m.mu.Unlock()

	if !rtlSDRPresent() {
		reboot("RTL-SDR USB device not found after process crash")
		return true
	}
	if crashes, tooMany := m.recordCrash(); tooMany {
		reboot(fmt.Sprintf("process crashed %d times in %s", crashes, crashWindow))
		return true
	} else {
		slog.Info("RTL-SDR USB device still present, restarting process",
			"crashes_in_window", crashes, "reboot_threshold", maxCrashesBeforeReboot)
	}

	m.logRestart("process crashed unexpectedly")

	// Release the dead process before starting a new one.
	m.mu.Lock()
	if m.logFile != nil {
		m.logFile.Close()
		m.logFile = nil
	}
	m.cmd = nil
	m.lastStart = time.Time{}
	m.mu.Unlock()

	time.Sleep(2 * time.Second)
	if err := m.Start(); err != nil {
		slog.Error("watchdog failed to restart after crash", "error", err)
	}
	return false
}

// handleStuckStopped reboots if the radio has been stopped and not recovering
// for too long — the symptom of a USB controller that will not come back.
func (m *Manager) handleStuckStopped() bool {
	m.mu.Lock()
	if m.stoppedSince.IsZero() {
		m.stoppedSince = time.Now()
		m.mu.Unlock()
		slog.Warn("radio process is stopped and not recovering",
			"reboot_in", stoppedRebootAfter)
		return false
	}
	stuckFor := time.Since(m.stoppedSince)
	m.mu.Unlock()

	if stuckFor >= stoppedRebootAfter {
		reboot(fmt.Sprintf("radio process stuck in stopped state for %.0fs", stuckFor.Seconds()))
		return true
	}
	slog.Warn("radio process still stopped",
		"stuck_for", stuckFor.Round(time.Second), "reboot_after", stoppedRebootAfter)
	return false
}

// handleFrozen responds to a live process that has stopped producing output.
func (m *Manager) handleFrozen() bool {
	if !rtlSDRPresent() {
		reboot("RTL-SDR USB device not found (process frozen)")
		return true
	}
	// Frozen restarts count against the same budget as crashes.
	if crashes, tooMany := m.recordCrash(); tooMany {
		reboot(fmt.Sprintf("process failed %d times in %s (frozen)", crashes, crashWindow))
		return true
	}

	slog.Warn("process appears frozen, restarting")
	m.watchdogRestart("frozen process detected")
	return false
}

// recordCrash adds a failure to the sliding window and reports the count along
// with whether it has reached the reboot threshold.
func (m *Manager) recordCrash() (int, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	m.crashTimes = append(m.crashTimes, now)
	recent := m.crashTimes[:0]
	for _, t := range m.crashTimes {
		if now.Sub(t) < crashWindow {
			recent = append(recent, t)
		}
	}
	m.crashTimes = recent
	return len(m.crashTimes), len(m.crashTimes) >= maxCrashesBeforeReboot
}

// watchdogRestart restarts the process without stopping the watchdog, which
// cannot wait on the goroutine it is running in.
func (m *Manager) watchdogRestart(reason string) {
	slog.Info("watchdog restart", "reason", reason)
	m.logRestart(reason)

	if err := m.Stop(false); err != nil {
		slog.Error("watchdog restart failed to stop process", "reason", reason, "error", err)
		return
	}
	time.Sleep(2 * time.Second)
	if err := m.Start(); err != nil {
		slog.Error("watchdog restart failed to start process", "reason", reason, "error", err)
	}
}

// logRestart records the restart and how long the process had been up.
func (m *Manager) logRestart(reason string) {
	m.mu.Lock()
	var uptime *int
	if !m.lastStart.IsZero() {
		seconds := int(time.Since(m.lastStart).Seconds())
		uptime = &seconds
	}
	m.mu.Unlock()

	if err := m.store.LogRestart(reason, uptime); err != nil {
		slog.Error("could not log restart", "reason", reason, "error", err)
	}
}

// isProcessFrozen reports whether the log file has gone quiet for long enough
// that dsd-fme is presumed wedged.
func (m *Manager) isProcessFrozen() bool {
	info, err := os.Stat(LogFile)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Error("error checking log file", "error", err)
		}
		return false
	}
	idle := time.Since(info.ModTime())
	if idle > frozenTimeout {
		slog.Warn("log file has gone quiet",
			"idle", idle.Round(time.Second), "threshold", frozenTimeout)
		return true
	}
	return false
}

// rotateLogIfNeeded rotates dsd-fme.jsonl once it exceeds the size limit.
// Returns true if a restart was triggered — dsd-fme holds the old file handle,
// so it has to be restarted to write to the new file.
func (m *Manager) rotateLogIfNeeded() bool {
	info, err := os.Stat(LogFile)
	if err != nil || info.Size() < logMaxSize {
		return false
	}

	slog.Info("rotating radio log",
		"size_mb", info.Size()/1024/1024, "limit_mb", logMaxSize/1024/1024)

	// Drop the oldest backup, then shift the rest down: .1 -> .2, current -> .1
	oldest := fmt.Sprintf("%s.%d", LogFile, logBackupCount)
	if err := os.Remove(oldest); err != nil && !os.IsNotExist(err) {
		slog.Warn("could not remove oldest log backup", "path", oldest, "error", err)
	}
	for i := logBackupCount - 1; i >= 1; i-- {
		src := fmt.Sprintf("%s.%d", LogFile, i)
		dst := fmt.Sprintf("%s.%d", LogFile, i+1)
		if err := os.Rename(src, dst); err != nil && !os.IsNotExist(err) {
			slog.Warn("could not rotate log backup", "from", src, "to", dst, "error", err)
		}
	}
	if err := os.Rename(LogFile, LogFile+".1"); err != nil {
		slog.Error("log rotation failed", "error", err)
		return false
	}

	slog.Info("log rotated, restarting dsd-fme to pick up the new log file")
	m.watchdogRestart("log rotation")
	return true
}
