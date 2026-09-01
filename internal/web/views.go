package web

import (
	"fmt"
	"time"

	"github.com/rossnoah/radiobot/internal/radio"
	"github.com/rossnoah/radiobot/internal/transcribe"
)

// weekdayHeaders labels the calendar columns. calendar.monthcalendar in the
// Python version started its weeks on Monday, and buildWeeks matches it.
var weekdayHeaders = []string{"M", "T", "W", "T", "F", "S", "S"}

// radioView is the radio card shared by the index and status pages. Uptime and
// LastMessage are empty when unknown; the index renders a placeholder, the
// status page hides the row.
type radioView struct {
	Running     bool
	Frequency   string
	Uptime      string
	LastMessage string
}

// transcriptionView is the transcription engine badge.
type transcriptionView struct {
	Deepgram      bool
	FallbackSince string
}

type dayLink struct {
	Key       string
	Label     string
	Available bool
}

type dayCell struct {
	Day       int
	Key       string
	Available bool
	Empty     bool
}

type monthView struct {
	Label    string
	Weekdays []string
	Weeks    [][]dayCell
	Open     bool
}

type indexView struct {
	Branding      string
	Radio         radioView
	Transcription transcriptionView
	RecentDays    []dayLink
	Months        []monthView
}

type fileRow struct {
	Filename   string
	Time       string
	Transcript string
	UnitName   string
}

type filesView struct {
	Branding      string
	Date          string
	FormattedDate string
	Files         []fileRow
}

type searchRow struct {
	Time          string
	TimestampDate string
	Transcript    string
	Filename      string
	Date          string
	UnitName      string
	HasFile       bool
}

type searchView struct {
	Branding string
	Query    string
	Results  []searchRow
}

type restartRow struct {
	Timestamp string
	Reason    string
	UpFor     string
}

type statusView struct {
	Branding        string
	Radio           radioView
	Transcription   transcriptionView
	TotalRecordings int
	StorageUsed     string
	Restarts        []restartRow
}

type unitOption struct {
	ID   int
	Name string
}

type testSuccess struct {
	Cleanup      bool
	Count        int
	FilesDeleted int
	Filename     string
	Date         string
}

type testView struct {
	Branding       string
	IsAuthed       bool
	Error          string
	Success        *testSuccess
	Units          []unitOption
	TestCount      int
	HasTestRecords bool
}

type loginView struct {
	Branding string
	Error    string
}

type errorView struct {
	Branding string
}

// newRadioView renders the radio status into display strings.
func newRadioView(status radio.Status) radioView {
	view := radioView{
		Running:   status.Running,
		Frequency: status.Config.FrequencyString(),
	}
	if status.UptimeSeconds != nil {
		view.Uptime = formatUptime(*status.UptimeSeconds)
	}
	if status.LastMessageSeconds != nil {
		view.LastMessage = formatAgo(*status.LastMessageSeconds)
	}
	return view
}

func newTranscriptionView(status transcribe.Status) transcriptionView {
	return transcriptionView{
		Deepgram:      status.Engine == "deepgram",
		FallbackSince: status.FallbackSince,
	}
}

// formatUptime renders seconds as "3h 05m".
func formatUptime(seconds int) string {
	return fmt.Sprintf("%dh %02dm", seconds/3600, (seconds%3600)/60)
}

// formatAgo renders how long ago something happened, coarsening as it recedes.
func formatAgo(seconds int) string {
	switch {
	case seconds < 60:
		return fmt.Sprintf("%ds ago", seconds)
	case seconds < 3600:
		return fmt.Sprintf("%dm ago", seconds/60)
	default:
		return fmt.Sprintf("%dh %02dm ago", seconds/3600, (seconds%3600)/60)
	}
}

// formatRestartUptime renders how long the radio had been up before a restart.
func formatRestartUptime(seconds int64) string {
	h := seconds / 3600
	m := (seconds % 3600) / 60
	s := seconds % 60
	switch {
	case h > 0:
		return fmt.Sprintf("%dh %02dm", h, m)
	case m > 0:
		return fmt.Sprintf("%dm %02ds", m, s)
	default:
		return fmt.Sprintf("%ds", s)
	}
}

// formatStorage renders a byte count in the largest unit that keeps it above 1.
func formatStorage(bytes int64) string {
	value := float64(bytes)
	units := []string{"B", "KB", "MB", "GB", "TB"}
	for _, unit := range units {
		if value < 1024 {
			return fmt.Sprintf("%.1f %s", value, unit)
		}
		value /= 1024
	}
	return fmt.Sprintf("%.1f TB", value*1024)
}

// buildWeeks lays a month out as calendar rows, padding with empty cells.
// Weeks start on Monday, matching Python's calendar.monthcalendar default.
func buildWeeks(year int, month time.Month, available map[string]struct{}) [][]dayCell {
	first := time.Date(year, month, 1, 0, 0, 0, 0, time.UTC)
	daysInMonth := first.AddDate(0, 1, -1).Day()

	// Weekday() is Sunday=0; shift so Monday=0.
	leading := (int(first.Weekday()) + 6) % 7

	var weeks [][]dayCell
	week := make([]dayCell, 0, 7)
	for i := 0; i < leading; i++ {
		week = append(week, dayCell{Empty: true})
	}
	for day := 1; day <= daysInMonth; day++ {
		key := fmt.Sprintf("%04d%02d%02d", year, month, day)
		_, ok := available[key]
		week = append(week, dayCell{Day: day, Key: key, Available: ok})
		if len(week) == 7 {
			weeks = append(weeks, week)
			week = make([]dayCell, 0, 7)
		}
	}
	if len(week) > 0 {
		for len(week) < 7 {
			week = append(week, dayCell{Empty: true})
		}
		weeks = append(weeks, week)
	}
	return weeks
}
