package radio

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeEscalator records escalations and controls what the supervisor does next.
type fakeEscalator struct {
	mu sync.Mutex

	reasons   []string
	recovered int

	// degradeAfter is how many escalations to answer with "shut down" before
	// switching to "carry on degraded". Zero degrades immediately.
	degradeAfter int
}

func (f *fakeEscalator) Escalate(reason string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reasons = append(f.reasons, reason)
	return len(f.reasons) > f.degradeAfter
}

func (f *fakeEscalator) Recovered() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recovered++
}

func (f *fakeEscalator) escalations() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.reasons...)
}

func (f *fakeEscalator) recoveries() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.recovered
}

// fakeDecoder installs a shell script in place of dsd-fme and returns nothing;
// the script's behaviour is given by body. It also writes to the log file the
// real decoder would, so the frozen check has something to look at.
func fakeDecoder(t *testing.T, body string) {
	t.Helper()

	script := filepath.Join(t.TempDir(), "fake-dsd-fme")
	contents := "#!/bin/sh\n" + body + "\n"
	if err := os.WriteFile(script, []byte(contents), 0o755); err != nil {
		t.Fatal(err)
	}

	original := dsdFMEBinary
	dsdFMEBinary = script
	t.Cleanup(func() { dsdFMEBinary = original })
}

// shortenBackoff makes the supervisor's timings test-sized.
func shortenBackoff(t *testing.T) {
	t.Helper()
	origCheck, origInitial, origMax, origHealthy, origDegraded, origSettle :=
		checkInterval, initialBackoff, maxBackoff, healthyRun, degradedBackoff, startSettleDelay

	checkInterval = 20 * time.Millisecond
	initialBackoff = 5 * time.Millisecond
	maxBackoff = 20 * time.Millisecond
	healthyRun = 300 * time.Millisecond
	degradedBackoff = 20 * time.Millisecond
	startSettleDelay = 30 * time.Millisecond

	t.Cleanup(func() {
		checkInterval, initialBackoff, maxBackoff, healthyRun, degradedBackoff, startSettleDelay =
			origCheck, origInitial, origMax, origHealthy, origDegraded, origSettle
	})
}

// TestSupervisorRestartsACrashingDecoder is the core loop: a decoder that dies
// immediately gets restarted, repeatedly, and then escalated.
func TestSupervisorRestartsACrashingDecoder(t *testing.T) {
	inTempDir(t)
	shortenBackoff(t)
	// Touch the log so the process looks like it produced output, then exit.
	fakeDecoder(t, "echo crashing >> "+LogFile+"; exit 1")

	manager := New(testRadioConfig(), &fakeStore{})
	escalator := &fakeEscalator{degradeAfter: 0} // degrade on the first escalation

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		manager.Supervise(ctx, escalator)
	}()

	// Wait for the escalation that follows maxRestartsBeforeEscalation runs.
	waitFor(t, 10*time.Second, func() bool { return len(escalator.escalations()) >= 1 })

	cancel()
	<-done

	reasons := escalator.escalations()
	if len(reasons) == 0 {
		t.Fatal("a permanently crashing decoder never escalated")
	}
	if !strings.Contains(reasons[0], "consecutive failed runs") {
		t.Errorf("escalation reason = %q, want it to mention consecutive failures", reasons[0])
	}
}

// TestSupervisorEscalationStopsTheLoop covers the shutdown handoff: when the
// policy says "I am exiting", the supervisor must return rather than keep
// restarting into a process that is going away.
func TestSupervisorEscalationStopsTheLoop(t *testing.T) {
	inTempDir(t)
	shortenBackoff(t)
	fakeDecoder(t, "exit 1")

	manager := New(testRadioConfig(), &fakeStore{})
	// degradeAfter is large, so Escalate always answers "shutting down".
	escalator := &fakeEscalator{degradeAfter: 1000}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		manager.Supervise(ctx, escalator)
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the supervisor kept running after the policy said it was shutting down")
	}

	if got := len(escalator.escalations()); got != 1 {
		t.Errorf("escalated %d times, want exactly 1 before returning", got)
	}
}

// TestSupervisorLeavesAHealthyDecoderAlone guards against restart churn.
func TestSupervisorLeavesAHealthyDecoderAlone(t *testing.T) {
	inTempDir(t)
	shortenBackoff(t)
	// Keep writing to the log so the frozen check stays happy.
	fakeDecoder(t, "while true; do echo alive >> "+LogFile+"; sleep 0.05; done")

	store := &fakeStore{}
	manager := New(testRadioConfig(), store)
	escalator := &fakeEscalator{}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		manager.Supervise(ctx, escalator)
	}()

	waitFor(t, 2*time.Second, func() bool { return manager.IsRunning() })
	time.Sleep(500 * time.Millisecond)

	if !manager.IsRunning() {
		t.Error("a healthy decoder was not left running")
	}
	if got := escalator.escalations(); len(got) != 0 {
		t.Errorf("a healthy decoder escalated: %v", got)
	}

	cancel()
	<-done

	if got := store.restartCount(); got != 0 {
		t.Errorf("logged %d restarts for a healthy decoder, want 0", got)
	}
	if manager.IsRunning() {
		t.Error("the decoder was left running after the supervisor stopped")
	}
}

// TestSupervisorRestartsAFrozenDecoder covers the wedge case: the process is
// alive but has stopped producing output.
func TestSupervisorRestartsAFrozenDecoder(t *testing.T) {
	inTempDir(t)
	shortenBackoff(t)
	// Alive but silent: never touches the log.
	fakeDecoder(t, "sleep 60")

	cfg := testRadioConfig()
	frozenAfter := 1
	cfg.FrozenTimeoutSeconds = &frozenAfter // one second of silence is a wedge

	store := &fakeStore{}
	manager := New(cfg, store)
	escalator := &fakeEscalator{degradeAfter: 0}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		manager.Supervise(ctx, escalator)
	}()

	waitFor(t, 15*time.Second, func() bool { return store.restartCount() >= 2 })

	cancel()
	<-done

	for _, reason := range store.restartReasons() {
		if !strings.Contains(reason, "no output for") {
			t.Errorf("restart reason = %q, want it to cite the missing output", reason)
		}
	}
}

// TestSupervisorRecoversFromDegraded covers the other end of the ladder: once
// the decoder stays up, the device should announce that it is well again.
func TestSupervisorRecoversFromDegraded(t *testing.T) {
	inTempDir(t)
	shortenBackoff(t)

	// Crash until the marker file appears, then run happily.
	marker := filepath.Join(inTempDirPath(t), "recovered")
	fakeDecoder(t, fmt.Sprintf(
		"if [ -f %s ]; then while true; do echo alive >> %s; sleep 0.05; done; fi; exit 1",
		marker, LogFile))

	manager := New(testRadioConfig(), &fakeStore{})
	escalator := &fakeEscalator{degradeAfter: 0} // degrade rather than shut down

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		manager.Supervise(ctx, escalator)
	}()

	waitFor(t, 10*time.Second, func() bool { return len(escalator.escalations()) >= 1 })

	// Fix the "hardware".
	if err := os.WriteFile(marker, []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}

	waitFor(t, 15*time.Second, func() bool { return escalator.recoveries() >= 1 })

	cancel()
	<-done
}

// TestHeartbeatAdvances is what the systemd watchdog ping is gated on.
func TestHeartbeatAdvances(t *testing.T) {
	inTempDir(t)
	shortenBackoff(t)
	fakeDecoder(t, "while true; do echo alive >> "+LogFile+"; sleep 0.05; done")

	manager := New(testRadioConfig(), &fakeStore{})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		manager.Supervise(ctx, &fakeEscalator{})
	}()

	first := manager.Heartbeat()
	waitFor(t, 2*time.Second, func() bool { return manager.Heartbeat().After(first) })

	cancel()
	<-done
}

// TestHeartbeatAdvancesWhileFailing matters because a device stuck in backoff
// is not a hung device; withholding the ping there would restart it forever.
func TestHeartbeatAdvancesWhileFailing(t *testing.T) {
	inTempDir(t)
	shortenBackoff(t)
	fakeDecoder(t, "exit 1")

	manager := New(testRadioConfig(), &fakeStore{})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		manager.Supervise(ctx, &fakeEscalator{degradeAfter: 0})
	}()

	first := manager.Heartbeat()
	waitFor(t, 3*time.Second, func() bool { return manager.Heartbeat().After(first) })

	cancel()
	<-done
}

// TestSupervisorHandlesAMissingBinary covers the config-error case that used
// to reboot the machine every two minutes.
func TestSupervisorHandlesAMissingBinary(t *testing.T) {
	inTempDir(t)
	shortenBackoff(t)

	original := dsdFMEBinary
	dsdFMEBinary = filepath.Join(t.TempDir(), "does-not-exist")
	t.Cleanup(func() { dsdFMEBinary = original })

	manager := New(testRadioConfig(), &fakeStore{})
	escalator := &fakeEscalator{degradeAfter: 0}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		manager.Supervise(ctx, escalator)
	}()

	waitFor(t, 10*time.Second, func() bool { return len(escalator.escalations()) >= 1 })

	cancel()
	<-done

	if reasons := escalator.escalations(); !strings.Contains(reasons[0], "could not start") {
		t.Errorf("escalation reason = %q, want it to name the start failure", reasons[0])
	}
}

// waitFor polls until cond holds or the deadline passes.
func waitFor(t *testing.T, limit time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("condition was not met within %s", limit)
}

// inTempDirPath returns the working directory the test is running in.
func inTempDirPath(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return dir
}
