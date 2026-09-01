package db

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rossnoah/radiobot/internal/wavutil"
)

func openTestDB(t *testing.T) *DB {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func TestDateFromFilename(t *testing.T) {
	tests := []struct {
		filename string
		want     string
	}{
		{"files/20260408/20260408_123456_1_SRC_1.wav", "20260408"},
		{`files\20260408\20260408_123456_1_SRC_1.wav`, "20260408"},
		{"20260408_123456_1.wav", ""}, // no folder component
		{"files/notadate/x.wav", ""},
		{"files/2026040/x.wav", ""}, // wrong length
	}
	for _, tt := range tests {
		if got := DateFromFilename(tt.filename); got != tt.want {
			t.Errorf("DateFromFilename(%q) = %q, want %q", tt.filename, got, tt.want)
		}
	}
}

func TestSaveAndReadTranscript(t *testing.T) {
	store := openTestDB(t)

	duration := 4.25
	path := "files/20260408/20260408_123456_1_SRC_1001.wav"
	if err := store.SaveTranscript(path, "unit one responding", `{"raw":true}`, false, &duration); err != nil {
		t.Fatal(err)
	}

	got, err := store.Transcript(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != "unit one responding" {
		t.Errorf("transcript = %q", got)
	}

	// The date column should be derived from the path, so the date filter works.
	rows, err := store.ListTranscripts("20260408")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("listed %d rows for the date, want 1", len(rows))
	}
	if !rows[0].Duration.Valid || rows[0].Duration.Float64 != 4.25 {
		t.Errorf("duration = %v, want 4.25", rows[0].Duration)
	}
	if rows[0].IsFake {
		t.Error("a real transcript was stored as fake")
	}

	if rows, err := store.ListTranscripts("20260409"); err != nil || len(rows) != 0 {
		t.Errorf("listing a different date returned %d rows (err %v)", len(rows), err)
	}
}

func TestTranscriptMissingReturnsEmpty(t *testing.T) {
	store := openTestDB(t)
	got, err := store.Transcript("files/20260408/absent.wav")
	if err != nil {
		t.Fatalf("Transcript on a missing row returned an error: %v", err)
	}
	if got != "" {
		t.Errorf("transcript = %q, want empty", got)
	}
}

// TestSaveTranscriptReplaces covers the INSERT OR REPLACE on the unique
// filename, which is how a re-transcription updates in place.
func TestSaveTranscriptReplaces(t *testing.T) {
	store := openTestDB(t)
	path := "files/20260408/20260408_123456_1_SRC_1.wav"

	if err := store.SaveTranscript(path, "first", "{}", false, nil); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveTranscript(path, "second", "{}", false, nil); err != nil {
		t.Fatal(err)
	}

	rows, err := store.ListTranscripts("")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("stored %d rows, want 1", len(rows))
	}
	if rows[0].Transcript != "second" {
		t.Errorf("transcript = %q, want second", rows[0].Transcript)
	}
}

func TestSearchTranscripts(t *testing.T) {
	store := openTestDB(t)
	if err := store.SaveTranscript("files/20260408/a_1_SRC_1.wav", "structure fire on main", "{}", false, nil); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveTranscript("files/20260408/b_1_SRC_2.wav", "routine traffic stop", "{}", false, nil); err != nil {
		t.Fatal(err)
	}

	results, err := store.SearchTranscripts("fire")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Transcript != "structure fire on main" {
		t.Errorf("search returned %v", results)
	}

	if results, _ := store.SearchTranscripts("zzz"); len(results) != 0 {
		t.Errorf("search for a missing term returned %d rows", len(results))
	}
}

func TestTestTranscriptLifecycle(t *testing.T) {
	store := openTestDB(t)

	dir := t.TempDir()
	fakePath := filepath.Join(dir, "20260408_120000_1_SRC_9999.wav")
	if err := os.WriteFile(fakePath, []byte("wav"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveTranscript(fakePath, "injected", "{}", true, nil); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveTranscript("files/20260408/real_SRC_1.wav", "real traffic", "{}", false, nil); err != nil {
		t.Fatal(err)
	}

	fakes, err := store.TestTranscripts()
	if err != nil {
		t.Fatal(err)
	}
	if len(fakes) != 1 || fakes[0].Transcript != "injected" {
		t.Fatalf("test transcripts = %v", fakes)
	}

	deleted, err := store.DeleteTestTranscripts()
	if err != nil {
		t.Fatal(err)
	}
	if len(deleted) != 1 || deleted[0] != fakePath {
		t.Errorf("deleted = %v, want [%s]", deleted, fakePath)
	}

	// The real recording must survive the cleanup.
	remaining, err := store.ListTranscripts("")
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 1 || remaining[0].Transcript != "real traffic" {
		t.Errorf("after cleanup, rows = %v", remaining)
	}
}

func TestRestartLog(t *testing.T) {
	store := openTestDB(t)

	uptime := 3600
	if err := store.LogRestart("frozen process detected", &uptime); err != nil {
		t.Fatal(err)
	}
	if err := store.LogRestart("process crashed unexpectedly", nil); err != nil {
		t.Fatal(err)
	}

	restarts, err := store.Restarts(100)
	if err != nil {
		t.Fatal(err)
	}
	if len(restarts) != 2 {
		t.Fatalf("logged %d restarts, want 2", len(restarts))
	}
	// Newest first.
	if restarts[0].Reason != "process crashed unexpectedly" {
		t.Errorf("first restart = %q", restarts[0].Reason)
	}
	if restarts[0].UptimeSeconds.Valid {
		t.Error("a nil uptime was stored as a value")
	}
	if !restarts[1].UptimeSeconds.Valid || restarts[1].UptimeSeconds.Int64 != 3600 {
		t.Errorf("uptime = %v, want 3600", restarts[1].UptimeSeconds)
	}

	if limited, _ := store.Restarts(1); len(limited) != 1 {
		t.Errorf("limit was not applied: got %d rows", len(limited))
	}
}

// TestMigrationsBackfillLegacyRows opens a database shaped like the one the
// Python version created before the duration and date columns existed, and
// checks that Open backfills both.
func TestMigrationsBackfillLegacyRows(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "legacy.db")

	recordingDir := filepath.Join(dir, "files", "20260408")
	if err := os.MkdirAll(recordingDir, 0o755); err != nil {
		t.Fatal(err)
	}
	recording := filepath.Join(recordingDir, "20260408_123456_1_SRC_1.wav")
	if err := wavutil.WriteSilence(recording, 8000, 2); err != nil {
		t.Fatal(err)
	}

	legacy, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Exec(`CREATE TABLE transcripts (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		filename TEXT UNIQUE NOT NULL,
		transcript TEXT NOT NULL,
		response TEXT NOT NULL,
		timestamp TEXT NOT NULL
	)`); err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Exec(
		`INSERT INTO transcripts (filename, transcript, response, timestamp) VALUES (?, ?, ?, ?)`,
		recording, "legacy row", "{}", "2026-04-08 12:34:56"); err != nil {
		t.Fatal(err)
	}
	// A row whose file is gone must not block the backfill.
	if _, err := legacy.Exec(
		`INSERT INTO transcripts (filename, transcript, response, timestamp) VALUES (?, ?, ?, ?)`,
		filepath.Join(recordingDir, "vanished_SRC_2.wav"), "orphan", "{}", "2026-04-08 12:35:00"); err != nil {
		t.Fatal(err)
	}
	legacy.Close()

	store, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open on a legacy database: %v", err)
	}
	defer store.Close()

	rows, err := store.ListTranscripts("20260408")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("backfilled date on %d rows, want 2", len(rows))
	}
	for _, row := range rows {
		if !row.Duration.Valid {
			t.Errorf("row %q still has a NULL duration", row.Filename)
		}
	}

	var backfilled Transcript
	for _, row := range rows {
		if row.Transcript == "legacy row" {
			backfilled = row
		}
	}
	if backfilled.Duration.Float64 != 2 {
		t.Errorf("duration = %v, want 2", backfilled.Duration.Float64)
	}
}

func TestBackupBookkeeping(t *testing.T) {
	store := openTestDB(t)

	if paths, err := store.UploadedPaths(); err != nil || len(paths) != 0 {
		t.Fatalf("fresh database reported %d uploads (err %v)", len(paths), err)
	}
	if marker := store.LastMarkerTime("__db_snapshot__"); !marker.IsZero() {
		t.Errorf("unset marker = %v, want the zero time", marker)
	}

	if err := store.MarkUploaded("files/20260408/a.wav", 1234); err != nil {
		t.Fatal(err)
	}
	paths, err := store.UploadedPaths()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := paths["files/20260408/a.wav"]; !ok {
		t.Errorf("uploaded paths = %v", paths)
	}

	if err := store.MarkUploaded("__db_snapshot__", 10); err != nil {
		t.Fatal(err)
	}
	marker := store.LastMarkerTime("__db_snapshot__")
	if time.Since(marker) > time.Minute || time.Since(marker) < -time.Minute {
		t.Errorf("marker time = %v, want roughly now", marker)
	}
}

// TestSnapshotTo checks the VACUUM INTO snapshot that the backup service
// uploads in place of Python's sqlite3 backup API.
func TestSnapshotTo(t *testing.T) {
	store := openTestDB(t)
	if err := store.SaveTranscript("files/20260408/a_SRC_1.wav", "snapshot me", "{}", false, nil); err != nil {
		t.Fatal(err)
	}

	snapshotPath := filepath.Join(t.TempDir(), "snapshot.db")
	if err := store.SnapshotTo(snapshotPath); err != nil {
		t.Fatalf("SnapshotTo: %v", err)
	}

	snapshot, err := Open(snapshotPath)
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Close()

	rows, err := snapshot.ListTranscripts("")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Transcript != "snapshot me" {
		t.Errorf("snapshot contents = %v", rows)
	}
}

// TestEscalationHistory is what stops a reboot loop: the record has to
// outlive both the process and the reboot systemd may perform.
func TestEscalationHistory(t *testing.T) {
	store := openTestDB(t)

	if count, err := store.EscalationsSince(time.Now().Add(-time.Hour)); err != nil || count != 0 {
		t.Fatalf("fresh database reported %d escalations (err %v)", count, err)
	}

	if err := store.RecordEscalation("process exited"); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordEscalation("the RTL-SDR is no longer on the USB bus"); err != nil {
		t.Fatal(err)
	}

	count, err := store.EscalationsSince(time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Errorf("counted %d escalations in the window, want 2", count)
	}

	// A window that starts after both records must count neither.
	count, err = store.EscalationsSince(time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Errorf("counted %d escalations in a future window, want 0", count)
	}

	// Newest first, for the dashboard.
	recent, err := store.Escalations(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(recent) != 2 {
		t.Fatalf("listed %d escalations, want 2", len(recent))
	}
	if recent[0].Reason != "the RTL-SDR is no longer on the USB bus" {
		t.Errorf("first escalation = %q, want the newest", recent[0].Reason)
	}
}

// TestEscalationHistorySurvivesReopen is the property the whole design leans
// on — systemd's start counter resets on boot, this must not.
func TestEscalationHistorySurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "persist.db")

	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RecordEscalation("process exited"); err != nil {
		t.Fatal(err)
	}
	store.Close()

	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()

	count, err := reopened.EscalationsSince(time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("counted %d escalations after reopening, want 1", count)
	}
}
