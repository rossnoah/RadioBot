package wavutil

import (
	"bytes"
	"encoding/binary"
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

// TestReadPCMRoundTrip checks the decoder against a file whose contents are
// known, since this is what feeds the on-device transcription model.
func TestReadPCMRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tone.wav")
	if err := WriteSilence(path, 8000, 1); err != nil {
		t.Fatal(err)
	}

	samples, rate, err := ReadPCM(path)
	if err != nil {
		t.Fatalf("ReadPCM: %v", err)
	}
	if rate != 8000 {
		t.Errorf("sample rate = %d, want 8000", rate)
	}
	if len(samples) != 8000 {
		t.Errorf("decoded %d samples, want 8000", len(samples))
	}
	for i, s := range samples {
		if s != 0 {
			t.Fatalf("sample %d of a silent file = %v, want 0", i, s)
		}
	}
}

// TestReadPCMNormalises checks the conversion to [-1, 1], which is the range
// the model expects.
func TestReadPCMNormalises(t *testing.T) {
	path := filepath.Join(t.TempDir(), "levels.wav")
	// Full-scale negative, silence, and near full-scale positive.
	writeSamples(t, path, 8000, 1, []int16{-32768, 0, 32767})

	samples, _, err := ReadPCM(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(samples) != 3 {
		t.Fatalf("decoded %d samples, want 3", len(samples))
	}
	if samples[0] != -1 {
		t.Errorf("full-scale negative decoded as %v, want -1", samples[0])
	}
	if samples[1] != 0 {
		t.Errorf("silence decoded as %v, want 0", samples[1])
	}
	if samples[2] <= 0.99 || samples[2] > 1 {
		t.Errorf("full-scale positive decoded as %v, want just under 1", samples[2])
	}
}

// TestReadPCMMixesStereoToMono covers the channel fold-down.
func TestReadPCMMixesStereoToMono(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stereo.wav")
	// Two frames: (+full, -full) averages to 0; (+full, +full) stays +full.
	writeSamples(t, path, 8000, 2, []int16{32767, -32767, 32767, 32767})

	samples, _, err := ReadPCM(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(samples) != 2 {
		t.Fatalf("decoded %d frames, want 2", len(samples))
	}
	if samples[0] > 1e-6 || samples[0] < -1e-6 {
		t.Errorf("opposed channels mixed to %v, want ~0", samples[0])
	}
	if samples[1] <= 0.99 {
		t.Errorf("matched channels mixed to %v, want ~1", samples[1])
	}
}

func TestReadPCMRejectsNonPCM(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.wav")
	if err := os.WriteFile(path, []byte("not a wav at all"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadPCM(path); err == nil {
		t.Error("ReadPCM accepted a file that is not a WAV")
	}
}

// writeSamples writes a 16-bit PCM WAV with the given interleaved samples.
func writeSamples(t *testing.T, path string, sampleRate, channels int, samples []int16) {
	t.Helper()

	dataSize := len(samples) * 2
	blockAlign := channels * 2
	var buf bytes.Buffer

	buf.WriteString("RIFF")
	binary.Write(&buf, binary.LittleEndian, uint32(36+dataSize))
	buf.WriteString("WAVEfmt ")
	binary.Write(&buf, binary.LittleEndian, uint32(16))
	binary.Write(&buf, binary.LittleEndian, uint16(1))
	binary.Write(&buf, binary.LittleEndian, uint16(channels))
	binary.Write(&buf, binary.LittleEndian, uint32(sampleRate))
	binary.Write(&buf, binary.LittleEndian, uint32(sampleRate*blockAlign))
	binary.Write(&buf, binary.LittleEndian, uint16(blockAlign))
	binary.Write(&buf, binary.LittleEndian, uint16(16))
	buf.WriteString("data")
	binary.Write(&buf, binary.LittleEndian, uint32(dataSize))
	for _, s := range samples {
		binary.Write(&buf, binary.LittleEndian, s)
	}

	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}
