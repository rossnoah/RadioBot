// Package radio supervises the dsd-fme process that drives the RTL-SDR receiver.
package radio

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/rossnoah/radiobot/internal/config"
)

const (
	PIDFile        = "dsd-fme.pid"
	LogFile        = "dsd-fme.jsonl"
	logMaxSize     = 100 * 1024 * 1024 // rotate past 100MB
	logBackupCount = 2                 // keep 2 old log files

	// Watchdog settings.
	frozenCheckInterval    = 30 * time.Second // how often to check process health
	frozenTimeout          = 30 * time.Minute // no log output for this long means frozen
	maxCrashesBeforeReboot = 3                // reboot after this many crashes in the window
	crashWindow            = 5 * time.Minute  // window for counting crashes
	stoppedRebootAfter     = 2 * time.Minute  // reboot if stuck in stopped state this long

	// RTL-SDR USB vendor ID (Realtek). Used to detect a dead USB controller —
	// on a Raspberry Pi 5 the only recovery is a reboot.
	rtlSDRUSBVendorID = "0bda"

	tempDir = "temp"
)

// Store is the subset of the database the manager writes to.
type Store interface {
	LogRestart(reason string, uptimeSeconds *int) error
}

// Status is the manager state rendered on the dashboard.
type Status struct {
	Running            bool
	PID                int
	UptimeSeconds      *int
	LastMessageSeconds *int
	Config             config.Radio
}

// Manager owns the dsd-fme process lifecycle and its watchdog.
type Manager struct {
	cfg   config.Radio
	store Store

	mu           sync.Mutex
	cmd          *exec.Cmd
	logFile      *os.File
	exited       chan struct{} // closed when the current process is reaped
	lastStart    time.Time
	lastMessage  time.Time
	stoppedSince time.Time
	crashTimes   []time.Time

	watchdogStop    chan struct{}
	watchdogDone    chan struct{}
	watchdogRunning bool
}

// New builds a manager for the given radio configuration.
func New(cfg config.Radio, store Store) *Manager {
	return &Manager{cfg: cfg, store: store}
}

// buildCommand assembles the dsd-fme invocation from configuration.
func (m *Manager) buildCommand() []string {
	// Input spec format: rtl:dev:freq:gain:ppm:bw:sq:vol
	rtlInput := fmt.Sprintf("rtl:%d:%sM:%d:%d:12:0:3",
		m.cfg.DeviceIndex, m.cfg.FrequencyString(), *m.cfg.Gain, m.cfg.PPM)

	return []string{
		"dsd-fme",
		"-fs", // DMR Stereo mode
		"-i", rtlInput,
		"-P", "-7", "./" + tempDir, // per-call wav files output directory
		"-Q", "dmr_log.jsonl", // DMR log file
		"-J", "events.txt", // events file
		"-a",      // auto-detect frame type
		"-t", "1", // frame timeout
		"-o", "null", // no audio output
	}
}

// Start launches the radio process and its watchdog.
func (m *Manager) Start() error {
	if m.IsRunning() {
		slog.Warn("radio process is already running")
		return nil
	}

	// Clear any orphaned dsd-fme processes still holding the USB device.
	m.killOrphans()

	if err := os.MkdirAll(tempDir, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", tempDir, err)
	}

	args := m.buildCommand()
	slog.Info("starting radio process", "command", strings.Join(args, " "))

	// dsd-fme writes its main output to stderr.
	logFile, err := os.OpenFile(LogFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("opening %s: %w", LogFile, err)
	}

	cmd := exec.Command(args[0], args[1:]...)
	cmd.Stderr = logFile
	cmd.Stdout = nil
	// Its own process group, so shutdown can take down any children too.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		logFile.Close()
		if errors.Is(err, exec.ErrNotFound) {
			slog.Error("dsd-fme command not found; ensure it is installed and in PATH")
		}
		return fmt.Errorf("starting radio process: %w", err)
	}

	exited := make(chan struct{})
	go func() {
		// Reaping the child is what makes IsRunning meaningful.
		_ = cmd.Wait()
		close(exited)
	}()

	// Give it a moment to fail fast on a bad device or busy USB.
	time.Sleep(time.Second)
	select {
	case <-exited:
		slog.Error("radio process failed to start; check dsd-fme.jsonl for details",
			"exit_code", cmd.ProcessState.ExitCode())
		logFile.Close()
		return errors.New("failed to start radio process; check logs for details")
	default:
	}

	m.mu.Lock()
	m.cmd = cmd
	m.logFile = logFile
	m.exited = exited
	m.lastStart = time.Now()
	m.stoppedSince = time.Time{} // clear stopped timer on successful start
	pid := cmd.Process.Pid
	m.mu.Unlock()

	// Record the PID so a restarted server can find an orphan from last time.
	if err := os.WriteFile(PIDFile, []byte(strconv.Itoa(pid)), 0o644); err != nil {
		slog.Warn("could not write PID file", "error", err)
	}

	slog.Info("radio process started", "pid", pid,
		"frequency_mhz", m.cfg.FrequencyString(), "gain", *m.cfg.Gain)
	slog.Info("radio output paths", "log", LogFile, "recordings", tempDir+"/")

	m.startWatchdog()
	return nil
}

// Stop terminates the radio process. stopWatchdog is false when the watchdog
// itself is driving the restart, since it cannot wait on its own goroutine.
func (m *Manager) Stop(stopWatchdog bool) error {
	if stopWatchdog {
		m.stopWatchdog()
	}

	m.mu.Lock()
	cmd, logFile, exited := m.cmd, m.logFile, m.exited
	m.mu.Unlock()

	if cmd == nil || cmd.Process == nil || isClosed(exited) {
		slog.Warn("radio process is not running")
		return nil
	}

	pid := cmd.Process.Pid
	slog.Info("stopping radio process", "pid", pid)

	pgid, err := syscall.Getpgid(pid)
	if err != nil {
		pgid = pid // process already gone, or never got its own group
	}

	// Graceful shutdown of the whole process group first.
	_ = syscall.Kill(-pgid, syscall.SIGTERM)

	select {
	case <-exited:
		slog.Info("radio process stopped gracefully")
	case <-time.After(5 * time.Second):
		slog.Warn("radio process did not stop gracefully, force killing")
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		<-exited
		slog.Info("radio process force killed")
	}

	m.mu.Lock()
	if logFile != nil {
		logFile.Close()
	}
	m.cmd = nil
	m.logFile = nil
	m.lastStart = time.Time{}
	m.mu.Unlock()

	if err := os.Remove(PIDFile); err != nil && !os.IsNotExist(err) {
		slog.Warn("could not remove PID file", "error", err)
	}
	return nil
}

// Restart stops and starts the radio process.
func (m *Manager) Restart() error {
	slog.Info("restarting radio process")
	if err := m.Stop(true); err != nil {
		return err
	}
	time.Sleep(time.Second)
	return m.Start()
}

// IsRunning reports whether the radio process is alive.
func (m *Manager) IsRunning() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cmd != nil && !isClosed(m.exited)
}

// RecordMessage notes that a transmission was received, for the status page.
func (m *Manager) RecordMessage() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastMessage = time.Now()
}

// Status returns the current state of the radio process.
func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()

	running := m.cmd != nil && !isClosed(m.exited)
	status := Status{Running: running, Config: m.cfg}
	if running {
		status.PID = m.cmd.Process.Pid
		if !m.lastStart.IsZero() {
			uptime := int(time.Since(m.lastStart).Seconds())
			status.UptimeSeconds = &uptime
		}
	}
	if !m.lastMessage.IsZero() {
		ago := int(time.Since(m.lastMessage).Seconds())
		status.LastMessageSeconds = &ago
	}
	return status
}

// killOrphans finds and kills dsd-fme processes left over from a previous run,
// which would otherwise hold the USB device open.
func (m *Manager) killOrphans() {
	if data, err := os.ReadFile(PIDFile); err == nil {
		if oldPID, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil {
			if isDSDFME(oldPID) {
				slog.Warn("killing orphaned dsd-fme process from PID file", "pid", oldPID)
				_ = syscall.Kill(oldPID, syscall.SIGTERM)
				time.Sleep(2 * time.Second)
				_ = syscall.Kill(oldPID, syscall.SIGKILL)
			}
		} else {
			slog.Warn("could not parse PID file", "error", err)
		}
		if err := os.Remove(PIDFile); err != nil && !os.IsNotExist(err) {
			slog.Warn("could not remove PID file", "error", err)
		}
	}

	// Catch anything the PID file did not know about.
	out, err := exec.Command("pgrep", "-f", "dsd-fme").Output()
	if err != nil {
		return // no matches, or pgrep unavailable
	}
	killed := false
	for _, line := range strings.Fields(string(out)) {
		pid, err := strconv.Atoi(line)
		if err != nil || pid == os.Getpid() {
			continue
		}
		slog.Warn("killing orphaned dsd-fme process", "pid", pid)
		_ = syscall.Kill(pid, syscall.SIGTERM)
		killed = true
	}
	if killed {
		time.Sleep(2 * time.Second)
	}
}

// isDSDFME reports whether a PID belongs to a dsd-fme process, so a recycled
// PID is not killed by mistake.
func isDSDFME(pid int) bool {
	cmdline, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		return false
	}
	return strings.Contains(string(cmdline), "dsd-fme")
}

// rtlSDRPresent reports whether an RTL-SDR device is visible on the USB bus.
// When the check cannot run (no sysfs, e.g. not Linux) it assumes present, so
// an unrelated failure never triggers a reboot.
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

// reboot restarts the machine to recover from a dead USB controller.
func reboot(reason string) {
	slog.Error("REBOOTING SYSTEM: USB controller has likely died and cannot recover without a reboot",
		"reason", reason)
	time.Sleep(2 * time.Second) // let logs flush
	if err := exec.Command("sudo", "reboot").Run(); err != nil {
		slog.Error("failed to reboot; manual intervention required", "error", err)
	}
}

func isClosed(ch chan struct{}) bool {
	if ch == nil {
		return true
	}
	select {
	case <-ch:
		return true
	default:
		return false
	}
}
