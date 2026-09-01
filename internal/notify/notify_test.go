package notify

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/rossnoah/radiobot/internal/config"
)

func TestContainsAny(t *testing.T) {
	words := []string{"Fire", "medical"}
	tests := []struct {
		message string
		want    bool
	}{
		{"we have a FIRE on third street", true}, // case-insensitive
		{"requesting Medical assistance", true},
		{"routine traffic stop", false},
		{"", false},
		{"firefighter responding", true}, // substring match, as before
	}
	for _, tt := range tests {
		if got := containsAny(tt.message, words); got != tt.want {
			t.Errorf("containsAny(%q) = %v, want %v", tt.message, got, tt.want)
		}
	}
}

func TestContainsAnyRepeated(t *testing.T) {
	words := []string{"mayday"}
	tests := []struct {
		message string
		minimum int
		want    bool
	}{
		{"mayday", 2, false},
		{"mayday mayday", 2, true},
		{"MAYDAY mayday Mayday", 2, true},
		{"mayday", 1, true},
		{"nothing here", 2, false},
	}
	for _, tt := range tests {
		if got := containsAnyRepeated(tt.message, words, tt.minimum); got != tt.want {
			t.Errorf("containsAnyRepeated(%q, min=%d) = %v, want %v",
				tt.message, tt.minimum, got, tt.want)
		}
	}
}

// TestEmptyWordsNeverMatch guards against a blank entry in the config word
// list turning every transmission into an alert.
func TestEmptyWordsNeverMatch(t *testing.T) {
	if containsAny("any message at all", []string{""}) {
		t.Error("an empty word matched")
	}
	if containsAnyRepeated("any message at all", []string{""}, 2) {
		t.Error("an empty word matched in the strict list")
	}
}

// capture collects the bodies posted to a stub webhook.
type capture struct {
	mu     sync.Mutex
	bodies []map[string]string
}

func (c *capture) handler(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var parsed map[string]string
	json.Unmarshal(body, &parsed)

	c.mu.Lock()
	defer c.mu.Unlock()
	c.bodies = append(c.bodies, parsed)
}

func (c *capture) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.bodies)
}

func TestCheckSendsDiscordOnMatch(t *testing.T) {
	captured := &capture{}
	server := httptest.NewServer(http.HandlerFunc(captured.handler))
	defer server.Close()

	cfg := config.Notifications{
		Discord: config.Discord{Enabled: true, WebhookURL: server.URL},
	}
	cfg.Wordlists.Standard.Words = []string{"fire"}

	notifier := New(cfg)
	notifier.Check("structure fire on main", "Unit 1")

	if captured.count() != 1 {
		t.Fatalf("posted %d times, want 1", captured.count())
	}
	got := captured.bodies[0]["content"]
	want := "structure fire on main\n\n[From: Unit 1]"
	if got != want {
		t.Errorf("content = %q, want %q", got, want)
	}
}

func TestCheckSkipsWhenDisabled(t *testing.T) {
	captured := &capture{}
	server := httptest.NewServer(http.HandlerFunc(captured.handler))
	defer server.Close()

	cfg := config.Notifications{
		Discord: config.Discord{Enabled: false, WebhookURL: server.URL},
	}
	cfg.Wordlists.Standard.Words = []string{"fire"}

	New(cfg).Check("structure fire on main", "Unit 1")

	if captured.count() != 0 {
		t.Errorf("posted %d times with Discord disabled, want 0", captured.count())
	}
}

func TestCheckSkipsWhenNoMatch(t *testing.T) {
	captured := &capture{}
	server := httptest.NewServer(http.HandlerFunc(captured.handler))
	defer server.Close()

	cfg := config.Notifications{
		Discord: config.Discord{Enabled: true, WebhookURL: server.URL},
	}
	cfg.Wordlists.Standard.Words = []string{"fire"}

	New(cfg).Check("routine traffic stop", "Unit 1")

	if captured.count() != 0 {
		t.Errorf("posted %d times without a keyword match, want 0", captured.count())
	}
}

// TestStrictListTriggersOnRepetition covers the second matching path.
func TestStrictListTriggersOnRepetition(t *testing.T) {
	captured := &capture{}
	server := httptest.NewServer(http.HandlerFunc(captured.handler))
	defer server.Close()

	cfg := config.Notifications{
		Discord: config.Discord{Enabled: true, WebhookURL: server.URL},
	}
	cfg.Wordlists.Strict.Words = []string{"help"}
	cfg.Wordlists.Strict.MinOccurrences = 2

	notifier := New(cfg)
	notifier.Check("help me", "Unit 1")
	if captured.count() != 0 {
		t.Error("a single occurrence triggered the strict list")
	}

	notifier.Check("help help someone", "Unit 1")
	if captured.count() != 1 {
		t.Errorf("posted %d times after a repeat, want 1", captured.count())
	}
}

// TestGroupMePostsBotID checks the GroupMe payload shape.
func TestGroupMePostsBotID(t *testing.T) {
	captured := &capture{}
	server := httptest.NewServer(http.HandlerFunc(captured.handler))
	defer server.Close()

	cfg := config.Notifications{GroupMe: config.GroupMe{Enabled: true, BotID: "bot-123"}}
	cfg.Wordlists.Standard.Words = []string{"fire"}

	notifier := New(cfg)
	notifier.client = server.Client()
	// Point the GroupMe post at the stub by overriding the package endpoint.
	original := groupmeEndpoint
	groupmeEndpoint = server.URL
	defer func() { groupmeEndpoint = original }()

	notifier.Check("fire alarm", "Unit 2")

	if captured.count() != 1 {
		t.Fatalf("posted %d times, want 1", captured.count())
	}
	if got := captured.bodies[0]["bot_id"]; got != "bot-123" {
		t.Errorf("bot_id = %q, want bot-123", got)
	}
	if got := captured.bodies[0]["text"]; got != "fire alarm\n\n[From: Unit 2]" {
		t.Errorf("text = %q", got)
	}
}

// TestAlertBypassesWordlists covers the device reporting on itself: an
// operational message must go out whatever the keyword configuration says.
func TestAlertBypassesWordlists(t *testing.T) {
	captured := &capture{}
	server := httptest.NewServer(http.HandlerFunc(captured.handler))
	defer server.Close()

	cfg := config.Notifications{
		Discord: config.Discord{Enabled: true, WebhookURL: server.URL},
	}
	// Deliberately empty: nothing here would ever match.
	cfg.Wordlists.Standard.Words = nil

	New(cfg).Alert("RadioBot has given up on the radio.")

	if captured.count() != 1 {
		t.Fatalf("posted %d times, want 1", captured.count())
	}
	got := captured.bodies[0]["content"]
	if got != "RadioBot has given up on the radio." {
		t.Errorf("content = %q, want the bare message with no unit suffix", got)
	}
}

func TestAlertSkipsDisabledServices(t *testing.T) {
	captured := &capture{}
	server := httptest.NewServer(http.HandlerFunc(captured.handler))
	defer server.Close()

	cfg := config.Notifications{
		Discord: config.Discord{Enabled: false, WebhookURL: server.URL},
	}
	New(cfg).Alert("nobody should hear this")

	if captured.count() != 0 {
		t.Errorf("posted %d times with every service disabled, want 0", captured.count())
	}
}

// TestConsoleModeSendsNothing is the guard that matters: these alerts reach
// real people, so a run against a real config must be able to withhold them.
func TestConsoleModeSendsNothing(t *testing.T) {
	captured := &capture{}
	server := httptest.NewServer(http.HandlerFunc(captured.handler))
	defer server.Close()

	cfg := config.Notifications{
		Discord: config.Discord{Enabled: true, WebhookURL: server.URL},
		GroupMe: config.GroupMe{Enabled: true, BotID: "bot-123"},
	}
	cfg.Wordlists.Standard.Words = []string{"fire"}

	notifier := NewConsole(cfg)
	notifier.client = server.Client()
	originalEndpoint := groupmeEndpoint
	groupmeEndpoint = server.URL
	defer func() { groupmeEndpoint = originalEndpoint }()

	// Both delivery paths, on a message that definitely matches.
	notifier.Check("structure fire on main", "Unit 1")
	notifier.Alert("RadioBot has given up on the radio.")

	if captured.count() != 0 {
		t.Errorf("console mode delivered %d notifications, want 0: %v",
			captured.count(), captured.bodies)
	}
}

// TestConsoleModeStillEvaluatesWordlists: console mode has to exercise the
// same decisions, or it is not a test of anything.
func TestConsoleModeStillEvaluatesWordlists(t *testing.T) {
	cfg := config.Notifications{
		Discord: config.Discord{Enabled: true, WebhookURL: "https://discord.example/webhooks/secret"},
	}
	cfg.Wordlists.Standard.Words = []string{"fire"}

	// Neither call may panic or reach the network; the difference between a
	// match and a miss is visible in the logs, not in behaviour here.
	notifier := NewConsole(cfg)
	notifier.Check("routine traffic stop", "Unit 1")
	notifier.Check("structure fire on main", "Unit 1")
}

// TestConsoleModeLogsHonestly guards the specific bug this replaced: the
// withheld path must not fall through to the "notification sent" log line.
func TestConsoleModeLogsHonestly(t *testing.T) {
	var buf bytes.Buffer
	original := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(original)

	cfg := config.Notifications{
		GroupMe: config.GroupMe{Enabled: true, BotID: "super-secret-bot-id"},
		Discord: config.Discord{Enabled: true, WebhookURL: "https://discord.example/webhooks/super-secret-token"},
	}
	NewConsole(cfg).Alert("RadioBot has given up on the radio.")

	logged := buf.String()
	if strings.Contains(logged, "notification sent") {
		t.Errorf("console mode claimed a delivery:\n%s", logged)
	}
	if !strings.Contains(logged, "withheld") {
		t.Errorf("console mode did not say the notification was withheld:\n%s", logged)
	}
	// Credentials must never reach the logs.
	for _, secret := range []string{"super-secret-bot-id", "super-secret-token"} {
		if strings.Contains(logged, secret) {
			t.Errorf("a credential leaked into the logs (%q):\n%s", secret, logged)
		}
	}
}

// TestDeliveryDoesNotLogCredentials covers the same rule on the failure path,
// where an unreachable service must not print the webhook it tried.
func TestDeliveryDoesNotLogCredentials(t *testing.T) {
	var buf bytes.Buffer
	original := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(original)

	cfg := config.Notifications{
		Discord: config.Discord{
			Enabled:    true,
			WebhookURL: "http://127.0.0.1:1/webhooks/super-secret-token",
		},
	}
	New(cfg).Alert("this will fail to send")

	if logged := buf.String(); strings.Contains(logged, "super-secret-token") {
		t.Errorf("a webhook credential leaked into the logs:\n%s", logged)
	}
}
