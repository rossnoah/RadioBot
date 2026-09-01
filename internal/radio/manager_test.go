package radio

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/rossnoah/radiobot/internal/config"
)

type fakeStore struct {
	mu       sync.Mutex
	restarts []string
}

func (f *fakeStore) LogRestart(reason string, uptimeSeconds *int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.restarts = append(f.restarts, reason)
	return nil
}

func testRadioConfig() config.Radio {
	gain := 32
	return config.Radio{Frequency: 461.375, Gain: &gain, DeviceIndex: 0, PPM: 0}
}

func TestBuildCommand(t *testing.T) {
	manager := New(testRadioConfig(), &fakeStore{})
	args := manager.buildCommand()

	if args[0] != "dsd-fme" {
		t.Fatalf("command = %q, want dsd-fme", args[0])
	}

	// The input spec is rtl:dev:freq:gain:ppm:bw:sq:vol.
	var input string
	for i, arg := range args {
		if arg == "-i" && i+1 < len(args) {
			input = args[i+1]
		}
	}
	if want := "rtl:0:461.375M:32:0:12:0:3"; input != want {
		t.Errorf("input spec = %q, want %q", input, want)
	}

	joined := fmt.Sprint(args)
	for _, want := range []string{"-fs", "-P", "-7", "-Q", "dmr_log.jsonl", "-J", "events.txt", "-a", "-t", "1", "-o", "null"} {
		if !contains(args, want) {
			t.Errorf("command %s is missing %q", joined, want)
		}
	}
}

func TestBuildCommandUsesDeviceAndPPM(t *testing.T) {
	cfg := testRadioConfig()
	cfg.DeviceIndex = 2
	cfg.PPM = -5
	gain := 0
	cfg.Gain = &gain

	args := New(cfg, &fakeStore{}).buildCommand()
	var input string
	for i, arg := range args {
		if arg == "-i" && i+1 < len(args) {
			input = args[i+1]
		}
	}
	if want := "rtl:2:461.375M:0:-5:12:0:3"; input != want {
		t.Errorf("input spec = %q, want %q", input, want)
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestStatusWhenStopped(t *testing.T) {
	status := New(testRadioConfig(), &fakeStore{}).Status()
	if status.Running {
		t.Error("a manager that was never started reports running")
	}
	if status.PID != 0 || status.UptimeSeconds != nil {
		t.Errorf("stopped status carries pid=%d uptime=%v", status.PID, status.UptimeSeconds)
	}
	if status.Config.FrequencyString() != "461.375" {
		t.Errorf("status frequency = %q", status.Config.FrequencyString())
	}
}

func TestRecordMessageUpdatesStatus(t *testing.T) {
	manager := New(testRadioConfig(), &fakeStore{})
	if manager.Status().LastMessageSeconds != nil {
		t.Fatal("last message is set before any message arrived")
	}

	manager.RecordMessage()
	last := manager.Status().LastMessageSeconds
	if last == nil || *last > 1 {
		t.Errorf("last message = %v, want roughly 0", last)
	}
}

// TestRecordCrashSlidingWindow covers the reboot threshold: only failures
// inside the window count.
func TestRecordCrashSlidingWindow(t *testing.T) {
	manager := New(testRadioConfig(), &fakeStore{})

	for i := 1; i < maxCrashesBeforeReboot; i++ {
		count, tooMany := manager.recordCrash()
		if count != i {
			t.Errorf("crash %d counted as %d", i, count)
		}
		if tooMany {
			t.Fatalf("reboot threshold hit early at crash %d", i)
		}
	}

	count, tooMany := manager.recordCrash()
	if count != maxCrashesBeforeReboot || !tooMany {
		t.Errorf("crash %d: count=%d tooMany=%v, want the threshold to trip", maxCrashesBeforeReboot, count, tooMany)
	}

	// Age every recorded crash out of the window; the count resets.
	manager.mu.Lock()
	for i := range manager.crashTimes {
		manager.crashTimes[i] = time.Now().Add(-2 * crashWindow)
	}
	manager.mu.Unlock()

	if count, tooMany := manager.recordCrash(); count != 1 || tooMany {
		t.Errorf("after the window elapsed: count=%d tooMany=%v, want 1/false", count, tooMany)
	}
}

func TestIsProcessFrozen(t *testing.T) {
	dir := t.TempDir()
	original, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(original)

	manager := New(testRadioConfig(), &fakeStore{})

	// No log file yet: not frozen, and not an error.
	if manager.isProcessFrozen() {
		t.Error("a missing log file was reported as frozen")
	}

	if err := os.WriteFile(LogFile, []byte("recent output\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if manager.isProcessFrozen() {
		t.Error("a freshly written log was reported as frozen")
	}

	stale := time.Now().Add(-2 * frozenTimeout)
	if err := os.Chtimes(LogFile, stale, stale); err != nil {
		t.Fatal(err)
	}
	if !manager.isProcessFrozen() {
		t.Error("a log idle for twice the timeout was not reported as frozen")
	}
}

// TestRotateLogIfNeeded checks the size threshold and the .1/.2 shuffle.
func TestRotateLogIfNeeded(t *testing.T) {
	dir := t.TempDir()
	original, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(original)

	manager := New(testRadioConfig(), &fakeStore{})

	// Under the limit: nothing happens.
	if err := os.WriteFile(LogFile, []byte("small"), 0o644); err != nil {
		t.Fatal(err)
	}
	if manager.rotateLogIfNeeded() {
		t.Error("rotated a log that is under the size limit")
	}

	// Over the limit. The manager has no process, so the restart it triggers
	// is a no-op; what matters is the files moving.
	oversized, err := os.Create(LogFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := oversized.Truncate(logMaxSize + 1); err != nil {
		t.Fatal(err)
	}
	oversized.Close()

	if err := os.WriteFile(LogFile+".1", []byte("previous"), 0o644); err != nil {
		t.Fatal(err)
	}

	if !manager.rotateLogIfNeeded() {
		t.Fatal("an oversized log was not rotated")
	}
	// The restart reopens the log, so what should be left behind is an empty
	// file rather than no file at all.
	if info, err := os.Stat(LogFile); err == nil && info.Size() != 0 {
		t.Errorf("the current log was not moved aside (size %d)", info.Size())
	}
	if info, err := os.Stat(LogFile + ".1"); err != nil || info.Size() <= logMaxSize {
		t.Error("the oversized log did not become .1")
	}
	previous, err := os.ReadFile(LogFile + ".2")
	if err != nil || string(previous) != "previous" {
		t.Errorf(".1 was not shifted to .2 (got %q, err %v)", previous, err)
	}
}

// TestKillOrphansRemovesStalePIDFile covers startup cleanup when the recorded
// PID no longer belongs to dsd-fme.
func TestKillOrphansRemovesStalePIDFile(t *testing.T) {
	dir := t.TempDir()
	original, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(original)

	// This process is certainly not dsd-fme, so it must not be signalled —
	// only the stale PID file should be cleaned up.
	if err := os.WriteFile(PIDFile, []byte(fmt.Sprint(os.Getpid())), 0o644); err != nil {
		t.Fatal(err)
	}

	New(testRadioConfig(), &fakeStore{}).killOrphans()

	if _, err := os.Stat(filepath.Join(dir, PIDFile)); !os.IsNotExist(err) {
		t.Error("the stale PID file was not removed")
	}
}

func TestStopWhenNotRunning(t *testing.T) {
	manager := New(testRadioConfig(), &fakeStore{})
	if err := manager.Stop(true); err != nil {
		t.Errorf("Stop on a manager that never started: %v", err)
	}
}

// TestRTLSDRPresentAssumesPresentWithoutSysfs keeps a machine with no sysfs
// (macOS, a container) from triggering a reboot.
func TestRTLSDRPresentAssumesPresentWithoutSysfs(t *testing.T) {
	if _, err := os.Stat("/sys/bus/usb/devices"); err == nil {
		t.Skip("this machine has sysfs; the fallback path is not exercised")
	}
	if !rtlSDRPresent() {
		t.Error("rtlSDRPresent reported absent on a machine that cannot check")
	}
}
