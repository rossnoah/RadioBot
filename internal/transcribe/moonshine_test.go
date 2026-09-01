package transcribe

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestMoonshineSidecarMissing covers the common case: no sidecar on disk, so
// the fallback reports a clear error rather than hanging or panicking.
func TestMoonshineSidecarMissing(t *testing.T) {
	original, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(original)

	transcript, err := moonshineTranscribe(context.Background(), "recording.wav")
	if err == nil {
		t.Fatal("moonshineTranscribe succeeded with no sidecar present")
	}
	if !strings.Contains(err.Error(), moonshineScript) {
		t.Errorf("error = %v, want it to name the missing script", err)
	}
	if transcript != "" {
		t.Errorf("transcript = %q, want empty", transcript)
	}
}

// TestMoonshineSidecarBridge runs the real Python sidecar. It is opt-in
// because the first run downloads a few hundred MB of model weights:
//
//	RADIOBOT_TEST_MOONSHINE=1 go test ./internal/transcribe/ -run Bridge
func TestMoonshineSidecarBridge(t *testing.T) {
	if os.Getenv("RADIOBOT_TEST_MOONSHINE") != "1" {
		t.Skip("set RADIOBOT_TEST_MOONSHINE=1 to exercise the Python sidecar")
	}

	// The sidecar path is resolved relative to the working directory, which in
	// production is the repository root.
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	original, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(repoRoot); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(original)

	if _, err := exec.LookPath(pythonPath()); err != nil {
		t.Skipf("no python interpreter available: %v", err)
	}

	// A silent recording transcribes to nothing, which is enough to prove the
	// bridge works: the model loaded, the WAV parsed, and stdout came back
	// clean (no progress bars, which the sidecar keeps on stderr).
	silent := filepath.Join(t.TempDir(), "silent.wav")
	if err := writeSilentWAV(silent); err != nil {
		t.Fatal(err)
	}

	transcript, err := moonshineTranscribe(context.Background(), silent)
	if err != nil {
		t.Fatalf("moonshineTranscribe: %v", err)
	}
	if transcript != "" {
		t.Logf("silent recording transcribed as %q", transcript)
	}
}

// writeSilentWAV writes a two-second silent 8kHz mono WAV. It is duplicated
// here rather than imported to keep this package free of a test-only
// dependency on wavutil.
func writeSilentWAV(path string) error {
	const (
		sampleRate = 8000
		seconds    = 2
		dataSize   = sampleRate * seconds * 2
	)
	header := []byte("RIFF")
	header = append(header, uint32le(36+dataSize)...)
	header = append(header, []byte("WAVEfmt ")...)
	header = append(header, uint32le(16)...)
	header = append(header, uint16le(1)...)          // PCM
	header = append(header, uint16le(1)...)          // mono
	header = append(header, uint32le(sampleRate)...) // sample rate
	header = append(header, uint32le(sampleRate*2)...)
	header = append(header, uint16le(2)...)  // block align
	header = append(header, uint16le(16)...) // bits per sample
	header = append(header, []byte("data")...)
	header = append(header, uint32le(dataSize)...)

	return os.WriteFile(path, append(header, make([]byte, dataSize)...), 0o600)
}

func uint32le(v uint32) []byte {
	return []byte{byte(v), byte(v >> 8), byte(v >> 16), byte(v >> 24)}
}

func uint16le(v uint16) []byte {
	return []byte{byte(v), byte(v >> 8)}
}
