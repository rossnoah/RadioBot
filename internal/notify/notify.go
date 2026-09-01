// Package notify sends alerts when a transcript contains configured keywords.
package notify

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/rossnoah/radiobot/internal/config"
)

const timeout = 5 * time.Second

// groupmeEndpoint is a variable so tests can point it at a stub server.
var groupmeEndpoint = "https://api.groupme.com/v3/bots/post"

// Notifier checks transcripts against the configured word lists and posts to
// whichever services are enabled.
type Notifier struct {
	cfg    config.Notifications
	client *http.Client

	// console prints notifications instead of delivering them. Every other
	// decision — the word lists, which services are enabled, the exact
	// message — is made the same way, so what is printed is what would have
	// been sent.
	console bool
}

// New builds a notifier that delivers to the configured services.
func New(cfg config.Notifications) *Notifier {
	return &Notifier{cfg: cfg, client: &http.Client{Timeout: timeout}}
}

// NewConsole builds a notifier that prints what it would have sent and sends
// nothing. Use it whenever the app is run against a real config for testing:
// the alerts go to real people, and a test transmission or a restart loop is
// not something to put in front of them.
func NewConsole(cfg config.Notifications) *Notifier {
	n := New(cfg)
	n.console = true
	return n
}

// Alert sends an operational message to every enabled service, bypassing the
// keyword lists. It is for the device reporting on itself — "I have given up
// on the radio" — rather than for anything heard over the air.
func (n *Notifier) Alert(message string) {
	if n.cfg.GroupMe.Enabled {
		n.sendGroupMe(message, "")
	}
	if n.cfg.Discord.Enabled {
		n.sendDiscord(message, "")
	}
}

// Check evaluates a transcript and sends notifications if it triggers an alert.
func (n *Notifier) Check(message, unitName string) {
	standard := n.cfg.Wordlists.Standard.Words
	strict := n.cfg.Wordlists.Strict.Words
	minOccurrences := n.cfg.Wordlists.Strict.MinOccurrences

	if !containsAny(message, standard) && !containsAnyRepeated(message, strict, minOccurrences) {
		return
	}

	if n.cfg.GroupMe.Enabled {
		n.sendGroupMe(message, unitName)
	}
	if n.cfg.Discord.Enabled {
		n.sendDiscord(message, unitName)
	}
}

// containsAny reports whether any target word appears in the string.
func containsAny(s string, words []string) bool {
	lower := strings.ToLower(s)
	for _, word := range words {
		if word == "" {
			continue
		}
		if strings.Contains(lower, strings.ToLower(word)) {
			return true
		}
	}
	return false
}

// containsAnyRepeated reports whether any target word appears at least
// minOccurrences times, which is how the strict word list avoids false alarms.
func containsAnyRepeated(s string, words []string, minOccurrences int) bool {
	if minOccurrences < 1 {
		minOccurrences = 1
	}
	lower := strings.ToLower(s)
	for _, word := range words {
		if word == "" {
			continue
		}
		if strings.Count(lower, strings.ToLower(word)) >= minOccurrences {
			return true
		}
	}
	return false
}

func (n *Notifier) sendGroupMe(message, unitName string) {
	if n.cfg.GroupMe.BotID == "" {
		slog.Info("no GroupMe bot ID configured")
		return
	}
	rendered := formatMessage(message, unitName)
	body := map[string]string{"text": rendered, "bot_id": n.cfg.GroupMe.BotID}
	n.deliver("GroupMe", groupmeEndpoint, body, rendered, unitName)
}

func (n *Notifier) sendDiscord(message, unitName string) {
	if n.cfg.Discord.WebhookURL == "" {
		slog.Info("no Discord webhook URL configured")
		return
	}
	rendered := formatMessage(message, unitName)
	n.deliver("Discord", n.cfg.Discord.WebhookURL, map[string]string{"content": rendered}, rendered, unitName)
}

// deliver is the single point where a notification either goes out or does
// not. Console mode stops here rather than inside post, so the log says what
// actually happened instead of reporting a delivery that never occurred.
//
// The rendered message is logged; the request body is not, because it carries
// the GroupMe bot ID, and a Discord webhook URL is itself a credential.
func (n *Notifier) deliver(service, url string, body any, rendered, unitName string) {
	if n.console {
		slog.Warn("notification withheld (console mode)",
			"service", service, "unit", unitName, "message", rendered)
		return
	}
	if err := n.post(url, body); err != nil {
		slog.Error(service+" notification failed", "error", sanitizeError(err))
		return
	}
	slog.Info(service+" notification sent", "service", service, "unit", unitName)
}

// sanitizeError strips the request URL out of a transport error. Go embeds
// the full URL in *url.Error, and a Discord webhook URL is itself a
// credential, so a failed send would otherwise print it to the logs.
func sanitizeError(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return fmt.Errorf("%s: %w", urlErr.Op, urlErr.Err)
	}
	return err
}

// formatMessage appends the transmitting unit. An empty unit means the message
// came from the device itself, not from the air, so there is nothing to append.
// formatMessage appends the transmitting unit. An empty unit means the message
// came from the device itself, not from the air, so there is nothing to append.
// redactURL keeps a Discord webhook secret out of the logs while still
// showing which service a withheld notification was for.
func redactURL(url string) string {
	if i := strings.Index(url, "://"); i >= 0 {
		rest := url[i+3:]
		if j := strings.Index(rest, "/"); j >= 0 {
			return url[:i+3] + rest[:j] + "/..."
		}
		return url
	}
	return url
}

func formatMessage(message, unitName string) string {
	if unitName == "" {
		return message
	}
	return message + "\n\n[From: " + unitName + "]"
}

func (n *Notifier) post(url string, body any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	resp, err := n.client.Post(url, "application/json", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return nil
}
