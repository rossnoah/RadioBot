package moonshine

import (
	"os"
	"testing"

	"github.com/rossnoah/radiobot/internal/wavutil"
)

// TestLiveTranscription runs the real library against real speech. It is
// opt-in because it needs libmoonshine on disk and downloads the model on
// first run:
//
//	MOONSHINE_LIB=/path/to/libmoonshine.dylib \
//	  RADIOBOT_TEST_MOONSHINE=1 go test ./internal/moonshine/ -run Live -v
func TestLiveTranscription(t *testing.T) {
	if os.Getenv("RADIOBOT_TEST_MOONSHINE") != "1" {
		t.Skip("set RADIOBOT_TEST_MOONSHINE=1 to exercise the real library")
	}
	audio := os.Getenv("RADIOBOT_TEST_WAV")
	if audio == "" {
		t.Skip("set RADIOBOT_TEST_WAV to a speech recording")
	}

	transcriber, err := Open(Options{ModelDir: os.Getenv("RADIOBOT_TEST_MODEL_DIR")})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer transcriber.Close()

	samples, rate, err := wavutil.ReadPCM(audio)
	if err != nil {
		t.Fatalf("ReadPCM: %v", err)
	}
	t.Logf("decoded %d samples at %d Hz", len(samples), rate)

	text, err := transcriber.Transcribe(samples, rate)
	if err != nil {
		t.Fatalf("Transcribe: %v", err)
	}
	if text == "" {
		t.Fatal("transcribed real speech to an empty string")
	}
	t.Logf("transcript: %q", text)

	// A second call must reuse the loaded model and not disturb the first.
	again, err := transcriber.Transcribe(samples, rate)
	if err != nil {
		t.Fatalf("second Transcribe: %v", err)
	}
	if again != text {
		t.Errorf("second transcription differed:\n  %q\n  %q", text, again)
	}
}
