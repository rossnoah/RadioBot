package wavutil

import (
	"math"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteSilenceThenDuration(t *testing.T) {
	tests := []struct {
		name       string
		sampleRate int
		seconds    float64
	}{
		{"two seconds at 8k", 8000, 2},
		{"fractional", 8000, 2.4},
		{"short", 8000, 0.25},
		{"zero", 8000, 0},
		{"higher rate", 44100, 1.5},
	}
	dir := t.TempDir()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(dir, tt.name+".wav")
			if err := WriteSilence(path, tt.sampleRate, tt.seconds); err != nil {
				t.Fatalf("WriteSilence: %v", err)
			}
			got, err := Duration(path)
			if err != nil {
				t.Fatalf("Duration: %v", err)
			}
			// A whole number of frames is written, so allow one frame of slop.
			if math.Abs(got-tt.seconds) > 1.0/float64(tt.sampleRate)+1e-9 {
				t.Errorf("Duration = %v, want ~%v", got, tt.seconds)
			}
		})
	}
}

func TestDurationRejectsNonWAV(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not.wav")
	if err := os.WriteFile(path, []byte("this is not a wav file at all"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Duration(path); err == nil {
		t.Error("Duration accepted a non-WAV file")
	}
}

func TestDurationMissingFile(t *testing.T) {
	if _, err := Duration(filepath.Join(t.TempDir(), "absent.wav")); err == nil {
		t.Error("Duration accepted a missing file")
	}
}

// TestDurationSkipsUnknownChunks covers WAV files that carry a LIST or fact
// chunk before the data chunk, which the header walk has to step over.
func TestDurationSkipsUnknownChunks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "extra.wav")
	if err := WriteSilence(path, 8000, 1); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	// Splice a 5-byte LIST chunk (odd size, so it carries a pad byte) in
	// between the fmt and data chunks.
	const headerEnd = 36 // RIFF(12) + fmt chunk(24)
	extra := []byte("LIST\x05\x00\x00\x00hello\x00")
	spliced := append([]byte{}, original[:headerEnd]...)
	spliced = append(spliced, extra...)
	spliced = append(spliced, original[headerEnd:]...)
	if err := os.WriteFile(path, spliced, 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := Duration(path)
	if err != nil {
		t.Fatalf("Duration: %v", err)
	}
	if math.Abs(got-1) > 1e-6 {
		t.Errorf("Duration = %v, want 1", got)
	}
}
