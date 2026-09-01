package transcribe

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Moonshine is a Python on-device speech model with no Go equivalent, so it
// runs as a sidecar: scripts/moonshine_transcribe.py takes a WAV path and
// prints the transcript to stdout. Loading the model takes a while on first
// use, hence the generous timeout.
const (
	moonshineScript  = "scripts/moonshine_transcribe.py"
	moonshineTimeout = 10 * time.Minute
)

// pythonPath finds the interpreter for the Moonshine sidecar: an explicit
// override first, then the project venv, then whatever is on PATH.
func pythonPath() string {
	if custom := os.Getenv("RADIOBOT_PYTHON"); custom != "" {
		return custom
	}
	venv := filepath.Join("venv", "bin", "python3")
	if _, err := os.Stat(venv); err == nil {
		abs, err := filepath.Abs(venv)
		if err == nil {
			return abs
		}
		return venv
	}
	return "python3"
}

// moonshineTranscribe shells out to the sidecar. It returns an empty string
// and an error on any failure; the caller records whatever it gets, matching
// the Python behavior of saving an empty transcript rather than dropping the
// recording.
func moonshineTranscribe(ctx context.Context, filePath string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, moonshineTimeout)
	defer cancel()

	if _, err := os.Stat(moonshineScript); err != nil {
		return "", fmt.Errorf("moonshine sidecar not found at %s: %w", moonshineScript, err)
	}

	cmd := exec.CommandContext(ctx, pythonPath(), moonshineScript, filePath)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("moonshine sidecar failed: %w: %s", err, truncate(strings.TrimSpace(stderr.String()), 500))
	}
	if msg := strings.TrimSpace(stderr.String()); msg != "" {
		slog.Debug("moonshine sidecar output", "stderr", truncate(msg, 500))
	}
	return strings.TrimSpace(stdout.String()), nil
}
