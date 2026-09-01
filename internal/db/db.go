// Package db owns the SQLite store for transcripts, restart history, and
// backup bookkeeping.
package db

import (
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/rossnoah/radiobot/internal/wavutil"
)

// File is the on-disk database path, relative to the working directory.
const File = "transcripts.db"

// timeLayout is the timestamp format stored in TEXT columns. It matches the
// Python implementation's strftime("%Y-%m-%d %H:%M:%S") so existing rows and
// new ones stay comparable.
const timeLayout = "2006-01-02 15:04:05"

// DB wraps the connection pool.
type DB struct {
	sql *sql.DB
}

// Transcript is one row of the transcripts table.
type Transcript struct {
	ID         int64
	Filename   string
	Transcript string
	Response   string
	Timestamp  string
	IsFake     bool
	Duration   sql.NullFloat64
	Date       sql.NullString
}

// Restart is one row of the restarts table.
type Restart struct {
	ID            int64
	Timestamp     string
	Reason        string
	UptimeSeconds sql.NullInt64
}

// Open connects to the database and runs schema setup and migrations.
func Open(path string) (*DB, error) {
	// WAL keeps the web reads from blocking the ingest writes; busy_timeout
	// stands in for the Python driver's timeout=10.
	dsn := path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(10000)"
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	d := &DB{sql: sqlDB}
	if err := d.init(); err != nil {
		sqlDB.Close()
		return nil, err
	}
	return d, nil
}

// Close releases the connection pool.
func (d *DB) Close() error { return d.sql.Close() }

// SQL exposes the underlying pool for the few callers that need raw access.
func (d *DB) SQL() *sql.DB { return d.sql }

func (d *DB) init() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS transcripts (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			filename TEXT UNIQUE NOT NULL,
			transcript TEXT NOT NULL,
			response TEXT NOT NULL,
			timestamp TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_filename ON transcripts (filename)`,
		`CREATE TABLE IF NOT EXISTS restarts (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			timestamp TEXT NOT NULL,
			reason TEXT NOT NULL,
			uptime_seconds INTEGER
		)`,
		`CREATE TABLE IF NOT EXISTS backup_uploads (
			path TEXT PRIMARY KEY,
			size INTEGER,
			uploaded_at TEXT NOT NULL
		)`,
	}
	for _, stmt := range stmts {
		if _, err := d.sql.Exec(stmt); err != nil {
			return fmt.Errorf("schema setup: %w", err)
		}
	}

	// Column migrations. These run against databases created by the Python
	// version, so a duplicate-column error just means the migration already ran.
	d.addColumn("is_fake", `ALTER TABLE transcripts ADD COLUMN is_fake INTEGER NOT NULL DEFAULT 0`)
	d.addColumn("duration", `ALTER TABLE transcripts ADD COLUMN duration REAL`)
	d.addColumn("date", `ALTER TABLE transcripts ADD COLUMN date TEXT`)

	if err := d.backfillDurations(); err != nil {
		slog.Error("duration backfill failed", "error", err)
	}
	if err := d.backfillDates(); err != nil {
		slog.Error("date backfill failed", "error", err)
	}

	if _, err := d.sql.Exec(`CREATE INDEX IF NOT EXISTS idx_date ON transcripts (date)`); err != nil {
		return fmt.Errorf("creating idx_date: %w", err)
	}
	return nil
}

func (d *DB) addColumn(name, stmt string) {
	if _, err := d.sql.Exec(stmt); err != nil {
		if !strings.Contains(strings.ToLower(err.Error()), "duplicate column") {
			slog.Error("column migration failed", "column", name, "error", err)
		}
	}
}

// backfillDurations fills in the duration column for rows written before it
// existed, reading each WAV header once. It runs until every row is filled.
func (d *DB) backfillDurations() error {
	var pending int
	if err := d.sql.QueryRow(`SELECT COUNT(*) FROM transcripts WHERE duration IS NULL`).Scan(&pending); err != nil {
		return err
	}
	if pending == 0 {
		return nil
	}
	slog.Info("backfilling duration for transcripts", "count", pending)

	rows, err := d.sql.Query(`SELECT id, filename FROM transcripts WHERE duration IS NULL`)
	if err != nil {
		return err
	}
	type record struct {
		id       int64
		filename string
	}
	var records []record
	for rows.Next() {
		var r record
		if err := rows.Scan(&r.id, &r.filename); err != nil {
			rows.Close()
			return err
		}
		records = append(records, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`UPDATE transcripts SET duration = ? WHERE id = ?`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, r := range records {
		// A missing or unreadable file records 0.0, matching the Python behavior.
		duration, _ := wavutil.Duration(r.filename)
		if _, err := stmt.Exec(duration, r.id); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	slog.Info("backfilled duration for transcripts", "count", len(records))
	return nil
}

// backfillDates fills in the date column from each row's filename path.
func (d *DB) backfillDates() error {
	var pending int
	if err := d.sql.QueryRow(`SELECT COUNT(*) FROM transcripts WHERE date IS NULL`).Scan(&pending); err != nil {
		return err
	}
	if pending == 0 {
		return nil
	}
	slog.Info("backfilling date for transcripts", "count", pending)

	rows, err := d.sql.Query(`SELECT id, filename FROM transcripts WHERE date IS NULL`)
	if err != nil {
		return err
	}
	type record struct {
		id   int64
		date string
	}
	var records []record
	for rows.Next() {
		var id int64
		var filename string
		if err := rows.Scan(&id, &filename); err != nil {
			rows.Close()
			return err
		}
		if date := DateFromFilename(filename); date != "" {
			records = append(records, record{id, date})
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`UPDATE transcripts SET date = ? WHERE id = ?`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, r := range records {
		if _, err := stmt.Exec(r.date, r.id); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	slog.Info("backfilled date for transcripts", "count", len(records))
	return nil
}

// DateFromFilename extracts the YYYYMMDD folder component from a transcript
// path such as files/20260408/20260408_123456_....wav.
func DateFromFilename(filename string) string {
	parts := strings.Split(strings.ReplaceAll(filename, `\`, "/"), "/")
	if len(parts) < 2 {
		return ""
	}
	candidate := parts[len(parts)-2]
	if len(candidate) != 8 {
		return ""
	}
	for _, r := range candidate {
		if r < '0' || r > '9' {
			return ""
		}
	}
	return candidate
}

// LogRestart records a radio restart event.
func (d *DB) LogRestart(reason string, uptimeSeconds *int) error {
	var uptime any
	if uptimeSeconds != nil {
		uptime = *uptimeSeconds
	}
	_, err := d.sql.Exec(
		`INSERT INTO restarts (timestamp, reason, uptime_seconds) VALUES (?, ?, ?)`,
		time.Now().Format(timeLayout), reason, uptime,
	)
	return err
}

// Restarts returns recent restart events, newest first.
func (d *DB) Restarts(limit int) ([]Restart, error) {
	rows, err := d.sql.Query(`SELECT id, timestamp, reason, uptime_seconds FROM restarts ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Restart
	for rows.Next() {
		var r Restart
		if err := rows.Scan(&r.ID, &r.Timestamp, &r.Reason, &r.UptimeSeconds); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SaveTranscript inserts or replaces a transcript row. A nil duration stores
// NULL, which the backfill will pick up later.
func (d *DB) SaveTranscript(filename, transcript, apiResponse string, isFake bool, duration *float64) error {
	var dur any
	if duration != nil {
		dur = *duration
	}
	var date any
	if s := DateFromFilename(filename); s != "" {
		date = s
	}
	fake := 0
	if isFake {
		fake = 1
	}
	_, err := d.sql.Exec(`
		INSERT OR REPLACE INTO transcripts (filename, transcript, response, timestamp, is_fake, duration, date)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		filename, transcript, apiResponse, time.Now().Format(timeLayout), fake, dur, date,
	)
	return err
}

// Transcript returns the transcript text for a filename, or "" if absent.
func (d *DB) Transcript(filename string) (string, error) {
	var text string
	err := d.sql.QueryRow(`SELECT transcript FROM transcripts WHERE filename = ?`, filename).Scan(&text)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return text, err
}

// ListTranscripts returns transcripts for a date (or all when date is empty),
// newest first.
func (d *DB) ListTranscripts(date string) ([]Transcript, error) {
	query := `SELECT id, filename, transcript, response, timestamp, is_fake, duration, date FROM transcripts`
	var args []any
	if date != "" {
		query += ` WHERE date = ?`
		args = append(args, date)
	}
	query += ` ORDER BY timestamp DESC`
	return d.queryTranscripts(query, args...)
}

// SearchTranscripts returns transcripts whose text contains the search string.
func (d *DB) SearchTranscripts(search string) ([]Transcript, error) {
	return d.queryTranscripts(
		`SELECT id, filename, transcript, response, timestamp, is_fake, duration, date
		 FROM transcripts WHERE transcript LIKE ? ORDER BY timestamp DESC`,
		"%"+search+"%",
	)
}

// TestTranscripts returns every injected test transcript.
func (d *DB) TestTranscripts() ([]Transcript, error) {
	return d.queryTranscripts(
		`SELECT id, filename, transcript, response, timestamp, is_fake, duration, date
		 FROM transcripts WHERE is_fake = 1 ORDER BY timestamp DESC`,
	)
}

// DeleteTestTranscripts removes every injected test transcript and returns the
// filenames that were deleted, so the caller can unlink the WAV files.
func (d *DB) DeleteTestTranscripts() ([]string, error) {
	tx, err := d.sql.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	rows, err := tx.Query(`SELECT filename FROM transcripts WHERE is_fake = 1`)
	if err != nil {
		return nil, err
	}
	var filenames []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return nil, err
		}
		filenames = append(filenames, name)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if _, err := tx.Exec(`DELETE FROM transcripts WHERE is_fake = 1`); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return filenames, nil
}

func (d *DB) queryTranscripts(query string, args ...any) ([]Transcript, error) {
	rows, err := d.sql.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Transcript
	for rows.Next() {
		var t Transcript
		var isFake int
		if err := rows.Scan(&t.ID, &t.Filename, &t.Transcript, &t.Response, &t.Timestamp, &isFake, &t.Duration, &t.Date); err != nil {
			return nil, err
		}
		t.IsFake = isFake != 0
		out = append(out, t)
	}
	return out, rows.Err()
}

// UploadedPaths returns the set of paths already backed up.
func (d *DB) UploadedPaths() (map[string]struct{}, error) {
	rows, err := d.sql.Query(`SELECT path FROM backup_uploads`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[string]struct{})
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			return nil, err
		}
		out[path] = struct{}{}
	}
	return out, rows.Err()
}

// MarkUploaded records that a path has been backed up.
func (d *DB) MarkUploaded(path string, size int64) error {
	_, err := d.sql.Exec(
		`INSERT OR REPLACE INTO backup_uploads (path, size, uploaded_at) VALUES (?, ?, ?)`,
		path, size, time.Now().Format(timeLayout),
	)
	return err
}

// LastMarkerTime returns when the given marker row was last uploaded. A marker
// that has never been written returns the zero time.
func (d *DB) LastMarkerTime(marker string) time.Time {
	var uploadedAt string
	err := d.sql.QueryRow(`SELECT uploaded_at FROM backup_uploads WHERE path = ?`, marker).Scan(&uploadedAt)
	if err != nil {
		return time.Time{}
	}
	parsed, err := time.ParseInLocation(timeLayout, uploadedAt, time.Local)
	if err != nil {
		return time.Time{}
	}
	return parsed
}

// SnapshotTo writes a consistent copy of the database to path. VACUUM INTO
// takes a transactionally consistent snapshot even while writes are in
// flight, which is what the Python version used the sqlite3 backup API for.
func (d *DB) SnapshotTo(path string) error {
	_, err := d.sql.Exec(`VACUUM INTO ?`, path)
	return err
}
