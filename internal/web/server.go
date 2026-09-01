// Package web serves the dashboard: recording lists, playback, search, status,
// and the test console.
package web

import (
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/rossnoah/radiobot/internal/config"
	"github.com/rossnoah/radiobot/internal/db"
	"github.com/rossnoah/radiobot/internal/processor"
	"github.com/rossnoah/radiobot/internal/radio"
	"github.com/rossnoah/radiobot/internal/transcribe"
	"github.com/rossnoah/radiobot/internal/util"
)

// pageCacheTTL is how long the index and status pages are reused. Both walk
// the recordings tree, which gets slow once there are tens of thousands.
const pageCacheTTL = 15 * time.Second

// RadioStatus reports the receiver state for the dashboard.
type RadioStatus interface {
	Status() radio.Status
}

// Transcriber reports which engine is currently transcribing.
type Transcriber interface {
	Status() transcribe.Status
}

// Server holds everything the handlers need.
type Server struct {
	cfg          *config.Config
	store        *db.DB
	radio        RadioStatus
	transcriber  Transcriber
	processor    *processor.Processor
	recordFolder string
	loginLog     *loginLog
	cache        *responseCache
	wsHandler    http.Handler
}

// New builds the HTTP server. wsHandler serves the live-update WebSocket.
func New(cfg *config.Config, store *db.DB, radioStatus RadioStatus, transcriber Transcriber,
	proc *processor.Processor, recordFolder string, wsHandler http.Handler) *Server {
	return &Server{
		cfg:          cfg,
		store:        store,
		radio:        radioStatus,
		transcriber:  transcriber,
		processor:    proc,
		recordFolder: recordFolder,
		loginLog:     newLoginLog(),
		cache:        newResponseCache(),
		wsHandler:    wsHandler,
	}
}

// Handler returns the router with every route registered.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /login", s.handleLoginForm)
	mux.HandleFunc("POST /login", s.handleLoginSubmit)

	mux.HandleFunc("GET /{$}", s.requirePassword(s.cached("index", s.handleIndex)))
	mux.HandleFunc("GET /files/{date}", s.requirePassword(s.handleFiles))
	mux.HandleFunc("GET /play/{date}/{filename}", s.requirePassword(s.handlePlay))
	mux.HandleFunc("GET /status", s.requirePassword(s.cached("status", s.handleStatus)))
	mux.HandleFunc("GET /search", s.requirePassword(s.handleSearch))
	mux.HandleFunc("GET /search/{query}", s.requirePassword(s.handleSearch))

	// The test console carries its own password, so it is not behind requirePassword.
	mux.HandleFunc("GET /test", s.handleTest)
	mux.HandleFunc("POST /test", s.handleTest)

	mux.Handle("/ws", s.wsHandler)

	// Anything unmatched falls through to the 404 page.
	mux.HandleFunc("/", s.handleNotFound)
	return mux
}

// Close releases server-owned resources.
func (s *Server) Close() error { return s.loginLog.Close() }

func (s *Server) handleNotFound(w http.ResponseWriter, r *http.Request) {
	render(w, http.StatusNotFound, "404.html", errorView{Branding: s.cfg.Application.Branding})
}

func (s *Server) handleLoginForm(w http.ResponseWriter, r *http.Request) {
	render(w, http.StatusOK, "login.html", loginView{Branding: s.cfg.Application.Branding})
}

func (s *Server) handleLoginSubmit(w http.ResponseWriter, r *http.Request) {
	password := r.FormValue("password")
	if !secretEqual(password, s.cfg.Application.Password) {
		s.loginLog.record(r, false, password)
		render(w, http.StatusOK, "login.html", loginView{
			Branding: s.cfg.Application.Branding,
			Error:    "Incorrect password",
		})
		return
	}

	s.loginLog.record(r, true, "")
	setAuthCookie(w, s.cfg.Application.Password)
	http.Redirect(w, r, safeNext(r.URL.Query().Get("next")), http.StatusFound)
}

// safeNext keeps the post-login redirect on this site: only a rooted path is
// accepted, so a crafted ?next= cannot bounce the user elsewhere.
func safeNext(next string) string {
	if next == "" || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") {
		return "/"
	}
	return next
}

func urlQueryEscape(s string) string { return url.QueryEscape(s) }

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	dateDirs, err := s.recordingDates()
	if err != nil {
		slog.Error("could not list recording dates", "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	available := make(map[string]struct{}, len(dateDirs))
	for _, date := range dateDirs {
		available[date] = struct{}{}
	}

	// Group into months in the order the dates appear (newest first), so the
	// most recent months are the ones opened by default.
	var months []monthView
	seen := make(map[string]struct{})
	for _, date := range dateDirs {
		if len(date) != 8 {
			continue
		}
		year, err := strconv.Atoi(date[:4])
		if err != nil {
			continue
		}
		monthNum, err := strconv.Atoi(date[4:6])
		if err != nil || monthNum < 1 || monthNum > 12 {
			continue
		}
		key := date[:6]
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}

		month := time.Month(monthNum)
		months = append(months, monthView{
			Label:    month.String() + " " + strconv.Itoa(year),
			Weekdays: weekdayHeaders,
			Weeks:    buildWeeks(year, month, available),
			Open:     len(months) < 3,
		})
	}

	recentDays := make([]dayLink, 0, 10)
	today := time.Now()
	for i := 0; i < 10; i++ {
		day := today.AddDate(0, 0, -i)
		key := day.Format("20060102")
		_, ok := available[key]
		recentDays = append(recentDays, dayLink{
			Key:       key,
			Label:     util.FormatDateDisplay(key),
			Available: ok,
		})
	}

	render(w, http.StatusOK, "index.html", indexView{
		Branding:      s.cfg.Application.Branding,
		Radio:         newRadioView(s.radio.Status()),
		Transcription: newTranscriptionView(s.transcriber.Status()),
		RecentDays:    recentDays,
		Months:        months,
	})
}

// recordingDates returns the date folders under the record folder, newest first.
func (s *Server) recordingDates() ([]string, error) {
	entries, err := os.ReadDir(s.recordFolder)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var dates []string
	for _, entry := range entries {
		if entry.IsDir() {
			dates = append(dates, entry.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(dates)))
	return dates, nil
}

func (s *Server) handleFiles(w http.ResponseWriter, r *http.Request) {
	date := r.PathValue("date")
	if !isDateFolder(date) {
		s.handleNotFound(w, r)
		return
	}

	folderPath := filepath.Join(s.recordFolder, date)
	entries, err := os.ReadDir(folderPath)
	if err != nil {
		s.handleNotFound(w, r)
		return
	}

	// Durations are cached in the database; only fall back to reading the
	// file header for recordings that have not been backfilled yet.
	transcripts, err := s.store.ListTranscripts(date)
	if err != nil {
		slog.Error("could not list transcripts", "date", date, "error", err)
	}
	byFilename := make(map[string]db.Transcript, len(transcripts))
	for _, t := range transcripts {
		byFilename[filepath.Base(t.Filename)] = t
	}

	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".wav") {
			names = append(names, entry.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names)))

	files := make([]fileRow, 0, len(names))
	for _, name := range names {
		record, hasRecord := byFilename[name]

		length := 0.0
		if hasRecord && record.Duration.Valid {
			length = record.Duration.Float64
		} else {
			length, _ = wavDuration(filepath.Join(folderPath, name))
		}
		if length < 0.5 {
			continue
		}

		row := fileRow{
			Filename: name,
			Time:     util.FormatTimeFromFilename(name),
		}
		if hasRecord {
			row.Transcript = record.Transcript
		}
		if uid, ok := util.RadioUIDFromFilename(name); ok {
			row.UnitName = s.cfg.UnitName(uid)
		}
		files = append(files, row)
	}

	render(w, http.StatusOK, "files.html", filesView{
		Branding:      s.cfg.Application.Branding,
		Date:          date,
		FormattedDate: util.FormatDateDisplay(date),
		Files:         files,
	})
}

func (s *Server) handlePlay(w http.ResponseWriter, r *http.Request) {
	date, filename := r.PathValue("date"), r.PathValue("filename")

	// Path components come straight from the URL, so reject anything that
	// could climb out of the recordings folder.
	if !isDateFolder(date) || !isSafeFilename(filename) {
		http.Error(w, "Invalid path", http.StatusBadRequest)
		return
	}

	path := filepath.Join(s.recordFolder, date, filename)
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		s.handleNotFound(w, r)
		return
	}

	// Recordings never change once written, so let browsers keep them.
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	http.ServeFile(w, r, path)
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	var totalBytes int64
	var totalRecordings int
	err := filepath.WalkDir(s.recordFolder, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable entries rather than abandoning the walk
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".wav") {
			return nil
		}
		totalRecordings++
		if info, err := entry.Info(); err == nil {
			totalBytes += info.Size()
		}
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		slog.Error("could not walk recordings folder", "error", err)
	}

	restarts, err := s.store.Restarts(100)
	if err != nil {
		slog.Error("could not read restart log", "error", err)
	}
	rows := make([]restartRow, 0, len(restarts))
	for _, restart := range restarts {
		row := restartRow{Timestamp: restart.Timestamp, Reason: restart.Reason, UpFor: "—"}
		if restart.UptimeSeconds.Valid {
			row.UpFor = formatRestartUptime(restart.UptimeSeconds.Int64)
		}
		rows = append(rows, row)
	}

	render(w, http.StatusOK, "status.html", statusView{
		Branding:        s.cfg.Application.Branding,
		Radio:           newRadioView(s.radio.Status()),
		Transcription:   newTranscriptionView(s.transcriber.Status()),
		TotalRecordings: totalRecordings,
		StorageUsed:     formatStorage(totalBytes),
		Restarts:        rows,
	})
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	query := r.PathValue("query")
	if query == "" {
		query = r.URL.Query().Get("query")
	}

	var results []searchRow
	if query != "" {
		matches, err := s.store.SearchTranscripts(query)
		if err != nil {
			slog.Error("transcript search failed", "query", query, "error", err)
		}
		results = make([]searchRow, 0, len(matches))
		for _, match := range matches {
			fullPath := strings.ReplaceAll(match.Filename, `\`, "/")
			filename := filepath.Base(fullPath)

			row := searchRow{
				Time:          util.FormatTimeFromFilename(filename),
				TimestampDate: firstField(match.Timestamp),
				Transcript:    match.Transcript,
				Filename:      filename,
			}
			if parts := strings.Split(fullPath, "/"); len(parts) >= 2 {
				row.Date = parts[len(parts)-2]
			}
			row.HasFile = row.Date != "" && row.Filename != ""
			if uid, ok := util.RadioUIDFromFilename(filename); ok {
				row.UnitName = s.cfg.UnitName(uid)
			}
			results = append(results, row)
		}
		// The query returns newest first; the page reads oldest first.
		reverse(results)
	}

	render(w, http.StatusOK, "search_results.html", searchView{
		Branding: s.cfg.Application.Branding,
		Query:    query,
		Results:  results,
	})
}

func firstField(s string) string {
	if fields := strings.Fields(s); len(fields) > 0 {
		return fields[0]
	}
	return s
}

func reverse(rows []searchRow) {
	for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
		rows[i], rows[j] = rows[j], rows[i]
	}
}

// isDateFolder reports whether a path component is a YYYYMMDD folder name.
// It doubles as path-traversal protection on URL components.
func isDateFolder(name string) bool {
	if len(name) != 8 {
		return false
	}
	for _, r := range name {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// isSafeFilename rejects anything that is not a plain name in one directory.
func isSafeFilename(name string) bool {
	if name == "" || name == "." || name == ".." {
		return false
	}
	if strings.ContainsAny(name, `/\`) || strings.Contains(name, "..") {
		return false
	}
	return true
}
