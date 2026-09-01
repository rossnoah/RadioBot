// Package util holds the filename and date conventions shared across the app.
//
// dsd-fme names each recording YYYYMMDD_HHMMSS_<seq>_DMR_..._<radio_uid>.wav,
// and those names are the only source of a transmission's time and unit.
package util

import (
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// FormatTimeFromFilename renders the time embedded in a recording filename as
// a 12-hour clock string. Anything unparseable falls back to the filename
// itself, so the UI always has something to show.
func FormatTimeFromFilename(filename string) string {
	parts := strings.Split(filename, "_")
	if len(parts) < 2 {
		return filename
	}

	timeStr := parts[1]
	var hours, minutes, seconds int
	var err error

	switch len(timeStr) {
	case 6:
		hours, minutes, seconds, err = threeFields(timeStr, 2, 2)
	case 5:
		// A single-digit hour, e.g. 93000 for 09:30:00.
		hours, minutes, seconds, err = threeFields(timeStr, 1, 2)
	default:
		return filename
	}
	if err != nil || hours > 23 || minutes > 59 || seconds > 59 {
		return filename
	}

	t := time.Date(0, 1, 1, hours, minutes, seconds, 0, time.UTC)
	return t.Format("03:04:05 PM")
}

// threeFields splits a digit run into hour/minute/second given the hour width.
func threeFields(s string, hourWidth, fieldWidth int) (int, int, int, error) {
	h, err := strconv.Atoi(s[:hourWidth])
	if err != nil {
		return 0, 0, 0, err
	}
	m, err := strconv.Atoi(s[hourWidth : hourWidth+fieldWidth])
	if err != nil {
		return 0, 0, 0, err
	}
	sec, err := strconv.Atoi(s[hourWidth+fieldWidth:])
	if err != nil {
		return 0, 0, 0, err
	}
	return h, m, sec, nil
}

// RadioUIDFromFilename extracts the trailing radio unit ID from a recording
// filename. The second return value is false when the name carries no ID.
func RadioUIDFromFilename(filename string) (int, bool) {
	base := strings.TrimSuffix(filepath.Base(filename), filepath.Ext(filename))
	parts := strings.Split(base, "_")
	uid, err := strconv.Atoi(parts[len(parts)-1])
	if err != nil {
		return 0, false
	}
	return uid, true
}

// DateFromFilename extracts and validates the YYYYMMDD prefix of a recording
// filename, returning "" if the name does not carry a plausible date.
func DateFromFilename(filename string) string {
	parts := strings.Split(filename, "_")
	if len(parts) < 2 || len(parts[0]) != 8 {
		return ""
	}
	stamp := parts[0]

	year, err := strconv.Atoi(stamp[:4])
	if err != nil {
		return ""
	}
	month, err := strconv.Atoi(stamp[4:6])
	if err != nil {
		return ""
	}
	day, err := strconv.Atoi(stamp[6:8])
	if err != nil {
		return ""
	}
	if year < 2000 || year > 2100 || month < 1 || month > 12 || day < 1 || day > 31 {
		return ""
	}
	return stamp
}

// FormatDateDisplay turns YYYYMMDD into YYYY/MM/DD.
func FormatDateDisplay(date string) string {
	if len(date) != 8 {
		return date
	}
	return date[:4] + "/" + date[4:6] + "/" + date[6:]
}

// FormatDuration renders seconds the way Python's timedelta does (H:MM:SS,
// with fractional seconds when present), since the templates display it raw.
func FormatDuration(seconds float64) string {
	// Round to 2 decimal places first, matching round(file_length, 2).
	rounded := float64(int64(seconds*100+0.5)) / 100
	whole := int64(rounded)
	frac := rounded - float64(whole)

	h := whole / 3600
	m := (whole % 3600) / 60
	s := whole % 60

	out := strconv.FormatInt(h, 10) + ":" +
		pad2(m) + ":" + pad2(s)
	if frac > 0 {
		// Python renders a non-zero fraction as exactly six digits, zeros and
		// all: str(timedelta(seconds=5.5)) is "0:00:05.500000".
		out += "." + pad6(int64(frac*1e6+0.5))
	}
	return out
}

func pad2(v int64) string {
	s := strconv.FormatInt(v, 10)
	if len(s) < 2 {
		return "0" + s
	}
	return s
}

func pad6(v int64) string {
	s := strconv.FormatInt(v, 10)
	for len(s) < 6 {
		s = "0" + s
	}
	return s
}
