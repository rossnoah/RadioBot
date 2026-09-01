// Package notify sends alerts when a transcript contains configured keywords.
package notify

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
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
}

// New builds a notifier from the notifications section of the config.
func New(cfg config.Notifications) *Notifier {
	return &Notifier{cfg: cfg, client: &http.Client{Timeout: timeout}}
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
	body := map[string]string{
		"text":   formatMessage(message, unitName),
		"bot_id": n.cfg.GroupMe.BotID,
	}
	if err := n.post(groupmeEndpoint, body); err != nil {
		slog.Error("GroupMe notification failed", "error", err)
		return
	}
	slog.Info("GroupMe notification sent", "unit", unitName)
}

func (n *Notifier) sendDiscord(message, unitName string) {
	if n.cfg.Discord.WebhookURL == "" {
		slog.Info("no Discord webhook URL configured")
		return
	}
	body := map[string]string{"content": formatMessage(message, unitName)}
	if err := n.post(n.cfg.Discord.WebhookURL, body); err != nil {
		slog.Error("Discord notification failed", "error", err)
		return
	}
	slog.Info("Discord notification sent", "unit", unitName)
}

func formatMessage(message, unitName string) string {
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
