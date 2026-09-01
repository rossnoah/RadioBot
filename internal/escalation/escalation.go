// Package escalation decides what happens when the radio cannot be recovered
// in-process.
//
// The escalation ladder has three rungs, and only the first belongs to this
// program:
//
//  1. radiobot restarts dsd-fme with backoff (internal/radio).
//  2. radiobot gives up and exits non-zero; systemd restarts the whole
//     service, which is strictly more powerful — a fresh process gets a fresh
//     libusb context and fresh file handles.
//  3. systemd reboots the machine once restarting stops helping, via
//     StartLimitBurst and StartLimitAction=reboot.
//
// systemd implements rungs 2 and 3 correctly, including the rate limiting.
// The one thing it cannot do is remember across a reboot that the reboot did
// not help — its start counter resets on boot. That is this package's job: it
// keeps a persistent record of escalations and, once there have been too many
// in one window, stops escalating altogether. The device then stays up in a
// degraded state, serving the dashboard and reporting itself over the
// configured notification channels, instead of reboot-looping until someone
// drives out to it.
package escalation

import (
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// Defaults chosen to sit just above systemd's own limits in the unit file, so
// systemd gets to try a reboot before this gives up entirely.
const (
	DefaultWindow = 2 * time.Hour
	DefaultLimit  = 3
)

// Store persists escalations across restarts and reboots.
type Store interface {
	RecordEscalation(reason string) error
	EscalationsSince(since time.Time) (int, error)
}

// Alerter delivers an operational message to whoever is watching the device.
type Alerter interface {
	Alert(message string)
}

// Policy applies the escalation budget.
type Policy struct {
	store   Store
	alerter Alerter
	window  time.Duration
	limit   int
	giveUp  func()

	mu             sync.Mutex
	degraded       bool
	degradedReason string
	degradedSince  time.Time
}

// New builds a policy. giveUp is called to shut the process down with a
// failure status, handing the problem to systemd; it may be nil in tests.
func New(store Store, alerter Alerter, giveUp func()) *Policy {
	return &Policy{
		store:   store,
		alerter: alerter,
		window:  DefaultWindow,
		limit:   DefaultLimit,
		giveUp:  giveUp,
	}
}

// Escalate reports that the radio could not be recovered in-process.
//
// It returns true when the caller should carry on in degraded mode: the
// escalation budget is spent, so handing the problem upwards again would only
// produce a restart or reboot loop. It returns false when the process is being
// torn down, in which case the caller should stop what it is doing and return.
func (p *Policy) Escalate(reason string) (degraded bool) {
	p.mu.Lock()
	if p.degraded {
		p.mu.Unlock()
		return true
	}
	p.mu.Unlock()

	recent, err := p.readBudget()
	if err != nil {
		// Escalate anyway: systemd's own start limit is still a backstop, and
		// refusing to escalate would leave a possibly-fixable radio down.
		slog.Error("could not read the escalation history; escalating anyway", "error", err)
	}

	if err == nil && recent >= p.limit {
		return p.enterDegraded(reason, recent)
	}

	if err := p.store.RecordEscalation(reason); err != nil {
		slog.Error("could not record the escalation", "error", err)
	}

	slog.Error("escalating to systemd: exiting so the service is restarted",
		"reason", reason, "escalations_in_window", recent+1, "limit", p.limit, "window", p.window)
	p.notify(fmt.Sprintf(
		"RadioBot is restarting the service: %s (escalation %d of %d in the last %s).",
		reason, recent+1, p.limit, p.window))

	if p.giveUp != nil {
		p.giveUp()
	}
	return false
}

// enterDegraded gives up on escalating and says so, once.
func (p *Policy) enterDegraded(reason string, recent int) bool {
	p.mu.Lock()
	if p.degraded {
		p.mu.Unlock()
		return true
	}
	p.degraded = true
	p.degradedReason = reason
	p.degradedSince = time.Now()
	p.mu.Unlock()

	slog.Error("escalation budget spent; staying up in degraded mode instead of restarting again",
		"reason", reason, "escalations_in_window", recent, "limit", p.limit, "window", p.window)
	p.notify(fmt.Sprintf(
		"RadioBot has given up on the radio: %s. There have been %d restarts in the last %s "+
			"and they are not helping, so the device is staying up in degraded mode. "+
			"The dashboard still works; the radio needs a look.",
		reason, recent, p.window))
	return true
}

func (p *Policy) readBudget() (int, error) {
	return p.store.EscalationsSince(time.Now().Add(-p.window))
}

func (p *Policy) notify(message string) {
	if p.alerter != nil {
		p.alerter.Alert(message)
	}
}

// Recovered clears the degraded state after the radio came back on its own.
// It is a no-op — and stays silent — if the device was never degraded.
func (p *Policy) Recovered() {
	p.mu.Lock()
	if !p.degraded {
		p.mu.Unlock()
		return
	}
	downFor := time.Since(p.degradedSince)
	p.degraded = false
	p.degradedReason = ""
	p.degradedSince = time.Time{}
	p.mu.Unlock()

	slog.Info("radio recovered; leaving degraded mode", "degraded_for", downFor.Round(time.Second))
	p.notify(fmt.Sprintf("RadioBot's radio is back after %s in degraded mode.",
		downFor.Round(time.Second)))
}

// Status describes the policy state for the dashboard.
type Status struct {
	Degraded bool
	Reason   string
	Since    time.Time
}

// Status reports whether the device has given up on recovering the radio.
func (p *Policy) Status() Status {
	p.mu.Lock()
	defer p.mu.Unlock()
	return Status{Degraded: p.degraded, Reason: p.degradedReason, Since: p.degradedSince}
}
