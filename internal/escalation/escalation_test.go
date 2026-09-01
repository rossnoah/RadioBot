package escalation

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeStore is an in-memory escalation history.
type fakeStore struct {
	mu       sync.Mutex
	recorded []time.Time
	reasons  []string
	readErr  error
}

func (f *fakeStore) RecordEscalation(reason string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recorded = append(f.recorded, time.Now())
	f.reasons = append(f.reasons, reason)
	return nil
}

func (f *fakeStore) EscalationsSince(since time.Time) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.readErr != nil {
		return 0, f.readErr
	}
	count := 0
	for _, at := range f.recorded {
		if !at.Before(since) {
			count++
		}
	}
	return count, nil
}

// backdate rewrites the history as though it happened long ago.
func (f *fakeStore) backdate(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.recorded {
		f.recorded[i] = f.recorded[i].Add(-d)
	}
}

type fakeAlerter struct {
	mu       sync.Mutex
	messages []string
}

func (f *fakeAlerter) Alert(message string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.messages = append(f.messages, message)
}

func (f *fakeAlerter) all() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.messages...)
}

// newPolicy builds a policy with a counting giveUp hook.
func newPolicy(t *testing.T) (*Policy, *fakeStore, *fakeAlerter, *int) {
	t.Helper()
	store := &fakeStore{}
	alerter := &fakeAlerter{}
	gaveUp := 0
	policy := New(store, alerter, func() { gaveUp++ })
	return policy, store, alerter, &gaveUp
}

// TestEscalateHandsOffUntilTheBudgetIsSpent is the whole point of the package:
// escalate a few times, then stop, so a fault that survives a restart or a
// reboot cannot become a loop.
func TestEscalateHandsOffUntilTheBudgetIsSpent(t *testing.T) {
	policy, store, alerter, gaveUp := newPolicy(t)

	// Each escalation is a separate process lifetime in production, so a fresh
	// policy reads the shared history each time.
	for i := 1; i <= DefaultLimit; i++ {
		p := New(store, alerter, func() { *gaveUp++ })
		if degraded := p.Escalate("process exited"); degraded {
			t.Fatalf("escalation %d degraded early", i)
		}
	}
	if *gaveUp != DefaultLimit {
		t.Errorf("gave up %d times, want %d", *gaveUp, DefaultLimit)
	}

	// The next one must refuse.
	final := New(store, alerter, func() { *gaveUp++ })
	if degraded := final.Escalate("process exited"); !degraded {
		t.Fatal("the policy escalated past its budget")
	}
	if *gaveUp != DefaultLimit {
		t.Errorf("gave up %d times after the budget was spent, want %d", *gaveUp, DefaultLimit)
	}

	if !final.Status().Degraded {
		t.Error("the policy did not report itself degraded")
	}

	// The operator has to be told, and the message has to say what to do.
	messages := alerter.all()
	last := messages[len(messages)-1]
	if !strings.Contains(last, "given up") {
		t.Errorf("final alert = %q, want it to say the device gave up", last)
	}
	_ = policy
}

// TestBudgetExpires covers the window: old escalations stop counting, so a
// device that fails once a month is never permanently degraded.
func TestBudgetExpires(t *testing.T) {
	store := &fakeStore{}
	alerter := &fakeAlerter{}

	for i := 0; i < DefaultLimit; i++ {
		New(store, alerter, func() {}).Escalate("process exited")
	}
	if degraded := New(store, alerter, func() {}).Escalate("process exited"); !degraded {
		t.Fatal("expected the budget to be spent")
	}

	// Age the history past the window.
	store.backdate(DefaultWindow + time.Minute)

	gaveUp := 0
	if degraded := New(store, alerter, func() { gaveUp++ }).Escalate("process exited"); degraded {
		t.Error("the policy stayed degraded after the window elapsed")
	}
	if gaveUp != 1 {
		t.Errorf("gave up %d times after the window elapsed, want 1", gaveUp)
	}
}

// TestDegradedIsAnnouncedOnce keeps a flapping radio from spamming the group.
func TestDegradedIsAnnouncedOnce(t *testing.T) {
	store := &fakeStore{}
	alerter := &fakeAlerter{}
	for i := 0; i < DefaultLimit; i++ {
		New(store, alerter, func() {}).Escalate("process exited")
	}

	policy := New(store, alerter, func() { t.Error("gave up after the budget was spent") })
	before := len(alerter.all())

	for i := 0; i < 5; i++ {
		if !policy.Escalate("process exited") {
			t.Fatal("expected degraded")
		}
	}

	if got := len(alerter.all()) - before; got != 1 {
		t.Errorf("sent %d alerts for repeated escalations, want 1", got)
	}
}

// TestRecoveredClearsDegraded covers the other end: the device should say so
// when the radio comes back.
func TestRecoveredClearsDegraded(t *testing.T) {
	store := &fakeStore{}
	alerter := &fakeAlerter{}
	for i := 0; i < DefaultLimit; i++ {
		New(store, alerter, func() {}).Escalate("process exited")
	}

	policy := New(store, alerter, func() {})
	if !policy.Escalate("process exited") {
		t.Fatal("expected degraded")
	}

	policy.Recovered()

	if policy.Status().Degraded {
		t.Error("Recovered did not clear the degraded state")
	}
	last := alerter.all()[len(alerter.all())-1]
	if !strings.Contains(last, "back") {
		t.Errorf("recovery alert = %q, want it to say the radio is back", last)
	}
}

// TestRecoveredIsSilentWhenHealthy avoids all-clear messages for an outage
// that never happened.
func TestRecoveredIsSilentWhenHealthy(t *testing.T) {
	_, _, alerter, _ := newPolicy(t)
	policy := New(&fakeStore{}, alerter, func() {})

	policy.Recovered()

	if got := alerter.all(); len(got) != 0 {
		t.Errorf("sent %v for a recovery that was never needed", got)
	}
}

// TestReadErrorStillEscalates: if the history cannot be read, escalating is
// the safer default — systemd's own start limit remains a backstop, whereas
// refusing would leave a fixable radio down.
func TestReadErrorStillEscalates(t *testing.T) {
	store := &fakeStore{readErr: errors.New("database is locked")}
	gaveUp := 0
	policy := New(store, &fakeAlerter{}, func() { gaveUp++ })

	if degraded := policy.Escalate("process exited"); degraded {
		t.Error("a history read error degraded the device")
	}
	if gaveUp != 1 {
		t.Errorf("gave up %d times, want 1", gaveUp)
	}
}

// TestEscalationReasonReachesTheAlert is what an operator actually reads.
func TestEscalationReasonReachesTheAlert(t *testing.T) {
	_, store, alerter, _ := newPolicy(t)
	policy := New(store, alerter, func() {})

	policy.Escalate("the RTL-SDR is no longer on the USB bus")

	messages := alerter.all()
	if len(messages) != 1 {
		t.Fatalf("sent %d alerts, want 1", len(messages))
	}
	if !strings.Contains(messages[0], "no longer on the USB bus") {
		t.Errorf("alert = %q, want it to carry the reason", messages[0])
	}
	if store.reasons[0] != "the RTL-SDR is no longer on the USB bus" {
		t.Errorf("stored reason = %q", store.reasons[0])
	}
}

// TestNilAlerterAndGiveUp keeps the policy usable in tests and CLI paths.
func TestNilAlerterAndGiveUp(t *testing.T) {
	policy := New(&fakeStore{}, nil, nil)
	if degraded := policy.Escalate("process exited"); degraded {
		t.Error("unexpectedly degraded")
	}
	policy.Recovered()
}
