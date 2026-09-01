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

func (f *fakeStore) restartCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.restarts)
}

func (f *fakeStore) restartReasons() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.restarts...)
}

// inTempDir runs the test in a scratch working directory, since the manager
// resolves the log and PID files relative to it.
func inTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	original, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(original) })
	return dir
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

func TestIdleFor(t *testing.T) {
	inTempDir(t)

	manager := New(testRadioConfig(), &fakeStore{})
	timeout := manager.frozenTimeout
	if timeout <= 0 {
		t.Fatal("the test config should have a frozen timeout")
	}

	// No log file yet: not frozen, and not an error.
	if _, frozen := manager.idleFor(); frozen {
		t.Error("a missing log file was reported as frozen")
	}

	if err := os.WriteFile(LogFile, []byte("recent output\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, frozen := manager.idleFor(); frozen {
		t.Error("a freshly written log was reported as frozen")
	}

	stale := time.Now().Add(-2 * timeout)
	if err := os.Chtimes(LogFile, stale, stale); err != nil {
		t.Fatal(err)
	}
	if _, frozen := manager.idleFor(); !frozen {
		t.Error("a log idle for twice the timeout was not reported as frozen")
	}
}

// TestIdleForDisabled covers frozen_timeout_seconds: 0, which switches the
// check off for setups where dsd-fme goes quiet on an idle channel.
func TestIdleForDisabled(t *testing.T) {
	inTempDir(t)

	cfg := testRadioConfig()
	disabled := 0
	cfg.FrozenTimeoutSeconds = &disabled
	manager := New(cfg, &fakeStore{})

	if err := os.WriteFile(LogFile, []byte("old output\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stale := time.Now().Add(-24 * time.Hour)
	if err := os.Chtimes(LogFile, stale, stale); err != nil {
		t.Fatal(err)
	}

	if _, frozen := manager.idleFor(); frozen {
		t.Error("the frozen check fired even though it is disabled")
	}
}

// TestRotateLog checks the size threshold and the .1/.2 shuffle.
func TestRotateLog(t *testing.T) {
	inTempDir(t)

	manager := New(testRadioConfig(), &fakeStore{})

	// Under the limit: nothing happens.
	if err := os.WriteFile(LogFile, []byte("small"), 0o644); err != nil {
		t.Fatal(err)
	}
	if manager.logNeedsRotation() {
		t.Error("a log under the size limit was marked for rotation")
	}

	// Over the limit.
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

	if !manager.logNeedsRotation() {
		t.Fatal("an oversized log was not marked for rotation")
	}
	if err := manager.rotateLog(); err != nil {
		t.Fatalf("rotateLog: %v", err)
	}
	if _, err := os.Stat(LogFile); !os.IsNotExist(err) {
		t.Error("the current log was not moved aside")
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
	dir := inTempDir(t)

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
	inTempDir(t)
	// Must not panic or block when there is no process to stop.
	New(testRadioConfig(), &fakeStore{}).stop()
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
