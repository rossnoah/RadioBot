package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rossnoah/radiobot/internal/config"
	"github.com/rossnoah/radiobot/internal/db"
	"github.com/rossnoah/radiobot/internal/escalation"
	"github.com/rossnoah/radiobot/internal/processor"
	"github.com/rossnoah/radiobot/internal/radio"
	"github.com/rossnoah/radiobot/internal/transcribe"
	"github.com/rossnoah/radiobot/internal/wavutil"
)

type fakeRadio struct{ status radio.Status }

func (f fakeRadio) Status() radio.Status { return f.status }

type fakeTranscriber struct{ status transcribe.Status }

func (f fakeTranscriber) Status() transcribe.Status                    { return f.status }
func (f fakeTranscriber) Transcribe(context.Context, string, *float64) {}

const testPassword = "hunter2"

// newTestServer builds a server over a temporary working directory holding one
// recording, and returns it with the record folder path.
func newTestServer(t *testing.T) (*Server, string) {
	t.Helper()

	// The login log and database are written relative to the working directory.
	workDir := t.TempDir()
	original, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(workDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(original) })

	store, err := db.Open(filepath.Join(workDir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	recordFolder := filepath.Join(workDir, "files")
	dateFolder := filepath.Join(recordFolder, "20251113")
	if err := os.MkdirAll(dateFolder, 0o755); err != nil {
		t.Fatal(err)
	}

	// One long recording that should be listed, and one too short to keep.
	longName := "20251113_200214_26522_DMR_CC_3_GROUP_TGT_1_SRC_1001.wav"
	shortName := "20251113_200300_26523_DMR_CC_3_GROUP_TGT_1_SRC_1001.wav"
	if err := wavutil.WriteSilence(filepath.Join(dateFolder, longName), 8000, 3); err != nil {
		t.Fatal(err)
	}
	if err := wavutil.WriteSilence(filepath.Join(dateFolder, shortName), 8000, 0.2); err != nil {
		t.Fatal(err)
	}
	duration := 3.0
	if err := store.SaveTranscript(filepath.Join(dateFolder, longName),
		"engine one responding to the call", "{}", false, &duration); err != nil {
		t.Fatal(err)
	}

	gain := 32
	cfg := &config.Config{
		Application: config.Application{
			Password:     testPassword,
			Branding:     "Test Radio",
			TestPassword: "letmein",
		},
		Radio: config.Radio{Frequency: 461.375, Gain: &gain},
		Units: map[int]string{1001: "Unit 1: John Doe"},
	}

	uptime := 3725
	lastMessage := 45
	status := radio.Status{
		Running: true, PID: 1234,
		UptimeSeconds: &uptime, LastMessageSeconds: &lastMessage,
		Config: cfg.Radio,
	}

	proc := processor.New(cfg, store, fakeTranscriber{}, nil, nil, nil)
	transcriber := fakeTranscriber{transcribe.Status{Engine: "deepgram"}}
	server := New(cfg, store, fakeRadio{status}, transcriber, proc, recordFolder, http.NotFoundHandler(), nil)
	t.Cleanup(func() { server.Close() })
	return server, recordFolder
}

// get issues a GET, optionally carrying the site auth cookie.
func get(t *testing.T, server *Server, path string, authed bool) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if authed {
		req.AddCookie(&http.Cookie{Name: siteCookie, Value: testPassword})
	}
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	return rec
}

func TestUnauthenticatedRequestsRedirectToLogin(t *testing.T) {
	server, _ := newTestServer(t)
	for _, path := range []string{"/", "/files/20251113", "/status", "/search", "/play/20251113/x.wav"} {
		rec := get(t, server, path, false)
		if rec.Code != http.StatusFound {
			t.Errorf("GET %s = %d, want 302", path, rec.Code)
		}
		if location := rec.Header().Get("Location"); !strings.HasPrefix(location, "/login?next=") {
			t.Errorf("GET %s redirected to %q, want /login?next=...", path, location)
		}
	}
}

func TestLoginSetsCookieAndRedirects(t *testing.T) {
	server, _ := newTestServer(t)

	req := httptest.NewRequest(http.MethodPost, "/login?next=%2Fstatus",
		strings.NewReader("password="+testPassword))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("login = %d, want 302", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != "/status" {
		t.Errorf("redirected to %q, want /status", got)
	}
	var found bool
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == siteCookie && cookie.Value == testPassword {
			found = true
			if !cookie.HttpOnly {
				t.Error("auth cookie is not HttpOnly")
			}
		}
	}
	if !found {
		t.Error("login did not set the auth cookie")
	}
}

func TestLoginRejectsWrongPassword(t *testing.T) {
	server, _ := newTestServer(t)

	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader("password=wrong"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("failed login = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Incorrect password") {
		t.Error("failed login did not render the error")
	}
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == siteCookie {
			t.Error("failed login set an auth cookie")
		}
	}
}

// TestLoginRejectsOffsiteNext guards against an open redirect through ?next=.
func TestLoginRejectsOffsiteNext(t *testing.T) {
	for _, next := range []string{"https://evil.example.com/", "//evil.example.com/", ""} {
		server, _ := newTestServer(t)
		req := httptest.NewRequest(http.MethodPost, "/login?next="+next,
			strings.NewReader("password="+testPassword))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		server.Handler().ServeHTTP(rec, req)

		if got := rec.Header().Get("Location"); got != "/" {
			t.Errorf("next=%q redirected to %q, want /", next, got)
		}
	}
}

func TestIndexRendersStatusAndDates(t *testing.T) {
	server, _ := newTestServer(t)
	rec := get(t, server, "/", true)

	if rec.Code != http.StatusOK {
		t.Fatalf("index = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"Test Radio",      // branding
		"461.375 MHz",     // frequency, without trailing zeros
		"Running",         // radio state
		"1h 02m",          // uptime, formatted like the Jinja version
		"45s ago",         // last message
		"Deepgram",        // transcription engine
		"/files/20251113", // the date that has recordings
		"November 2025",   // calendar month heading
	} {
		if !strings.Contains(body, want) {
			t.Errorf("index page missing %q", want)
		}
	}
}

func TestFilesPageListsRecordings(t *testing.T) {
	server, _ := newTestServer(t)
	rec := get(t, server, "/files/20251113", true)

	if rec.Code != http.StatusOK {
		t.Fatalf("files page = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "engine one responding to the call") {
		t.Error("files page missing the transcript")
	}
	if !strings.Contains(body, "Unit 1: John Doe") {
		t.Error("files page missing the unit name")
	}
	if !strings.Contains(body, "08:02:14 PM") {
		t.Error("files page missing the formatted time")
	}
	// The 0.2s recording is below the half-second floor.
	if strings.Contains(body, "20251113_200300") {
		t.Error("files page listed a recording shorter than 0.5s")
	}
	// socket.io is gone; the page should open a plain WebSocket.
	if strings.Contains(body, "socket.io") {
		t.Error("files page still references socket.io")
	}
	if !strings.Contains(body, "new WebSocket(") {
		t.Error("files page does not open a WebSocket")
	}
}

func TestFilesPageRejectsBadDate(t *testing.T) {
	server, _ := newTestServer(t)
	for _, date := range []string{"notadate", "2025111", "..%2F.."} {
		rec := get(t, server, "/files/"+date, true)
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET /files/%s = %d, want 404", date, rec.Code)
		}
	}
}

func TestPlayServesRecording(t *testing.T) {
	server, _ := newTestServer(t)
	rec := get(t, server, "/play/20251113/20251113_200214_26522_DMR_CC_3_GROUP_TGT_1_SRC_1001.wav", true)

	if rec.Code != http.StatusOK {
		t.Fatalf("play = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); !strings.Contains(got, "immutable") {
		t.Errorf("Cache-Control = %q, want an immutable directive", got)
	}
	if !strings.HasPrefix(rec.Body.String(), "RIFF") {
		t.Error("play did not serve WAV data")
	}
}

// TestPlayRejectsTraversal covers the path components the URL controls.
func TestPlayRejectsTraversal(t *testing.T) {
	server, _ := newTestServer(t)
	tests := []struct {
		path       string
		wantStatus int
	}{
		{"/play/20251113/..%2F..%2Fconfig.yaml", http.StatusBadRequest},
		{"/play/..%2F..%2Fetc/passwd", http.StatusBadRequest},
		{"/play/20251113/absent.wav", http.StatusNotFound},
	}
	for _, tt := range tests {
		rec := get(t, server, tt.path, true)
		if rec.Code != tt.wantStatus {
			t.Errorf("GET %s = %d, want %d", tt.path, rec.Code, tt.wantStatus)
		}
	}
}

func TestSearchFindsTranscript(t *testing.T) {
	server, _ := newTestServer(t)

	rec := get(t, server, "/search?query=responding", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("search = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "engine one responding to the call") {
		t.Error("search did not return the matching transcript")
	}
	if !strings.Contains(body, "/files/20251113#") {
		t.Error("search result is missing its in-context link")
	}

	rec = get(t, server, "/search?query=nothingmatchesthis", true)
	if !strings.Contains(rec.Body.String(), "No results found") {
		t.Error("empty search did not render the no-results message")
	}
}

// TestSearchPathForm covers the /search/<query> route the Python app also had.
func TestSearchPathForm(t *testing.T) {
	server, _ := newTestServer(t)
	rec := get(t, server, "/search/responding", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("search = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "engine one responding to the call") {
		t.Error("path-form search did not return the matching transcript")
	}
}

func TestStatusPage(t *testing.T) {
	server, _ := newTestServer(t)
	rec := get(t, server, "/status", true)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"System Status", "461.375 MHz", "1h 02m", "45s ago", "No restarts recorded yet"} {
		if !strings.Contains(body, want) {
			t.Errorf("status page missing %q", want)
		}
	}
	// Two recordings on disk, including the short one.
	if !strings.Contains(body, "Total Recordings") {
		t.Error("status page missing the recordings count")
	}
}

func TestNotFoundPage(t *testing.T) {
	server, _ := newTestServer(t)
	rec := get(t, server, "/nope", true)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown path = %d, want 404", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Page Not Found") {
		t.Error("404 page did not render")
	}
}

// TestTestConsoleRequiresItsOwnPassword checks that the console is gated
// independently of the site password.
func TestTestConsoleRequiresItsOwnPassword(t *testing.T) {
	server, _ := newTestServer(t)

	rec := get(t, server, "/test", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("test console = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "This page requires a separate password") {
		t.Error("test console is not showing its password gate to a site-authenticated user")
	}

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.AddCookie(&http.Cookie{Name: testCookie, Value: "letmein"})
	rec = httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), "Inject Test Transmission") {
		t.Error("test console did not unlock with its own cookie")
	}
	if !strings.Contains(rec.Body.String(), "Unit 1: John Doe (ID: 1001)") {
		t.Error("test console is missing the configured units")
	}
}

func TestPageCacheReusesRenderedResponse(t *testing.T) {
	server, recordFolder := newTestServer(t)

	first := get(t, server, "/", true)
	if first.Code != http.StatusOK {
		t.Fatalf("index = %d", first.Code)
	}

	// A new date folder should not appear until the cache expires.
	newFolder := filepath.Join(recordFolder, time.Now().Format("20060102"))
	if err := os.MkdirAll(newFolder, 0o755); err != nil {
		t.Fatal(err)
	}
	second := get(t, server, "/", true)
	if second.Body.String() != first.Body.String() {
		t.Error("index was re-rendered inside the cache window")
	}
}

// fakeHealth reports a fixed escalation state.
type fakeHealth struct{ status escalation.Status }

func (f fakeHealth) Status() escalation.Status { return f.status }

// TestStatusPageShowsDegradedBanner: when the device gives up, the status page
// is where the alert sends someone to look.
func TestStatusPageShowsDegradedBanner(t *testing.T) {
	server, _ := newTestServer(t)
	server.health = fakeHealth{escalation.Status{
		Degraded: true,
		Reason:   "the RTL-SDR is no longer on the USB bus",
		Since:    time.Date(2026, 9, 1, 3, 4, 5, 0, time.Local),
	}}

	body := get(t, server, "/status", true).Body.String()
	for _, want := range []string{
		"The radio needs attention.",
		"the RTL-SDR is no longer on the USB bus",
		"2026-09-01 03:04:05",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("degraded status page missing %q", want)
		}
	}
}

func TestStatusPageHidesBannerWhenHealthy(t *testing.T) {
	server, _ := newTestServer(t)
	server.health = fakeHealth{escalation.Status{Degraded: false}}

	if body := get(t, server, "/status", true).Body.String(); strings.Contains(body, "needs attention") {
		t.Error("a healthy device showed the degraded banner")
	}
}
