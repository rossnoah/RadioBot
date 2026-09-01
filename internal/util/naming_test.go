package util

import "testing"

func TestFormatTimeFromFilename(t *testing.T) {
	tests := []struct {
		name     string
		filename string
		want     string
	}{
		{"six digit time", "20251113_200214_26522_DMR_CC_3_GROUP_TGT_1_SRC_1.wav", "08:02:14 PM"},
		{"five digit time", "20251113_93000_26522_DMR_SRC_1.wav", "09:30:00 AM"},
		{"midnight", "20251113_000000_1_SRC_1.wav", "12:00:00 AM"},
		{"noon", "20251113_120000_1_SRC_1.wav", "12:00:00 PM"},
		{"no separator falls back", "recording.wav", "recording.wav"},
		{"bad time falls back", "20251113_xxxxxx_1.wav", "20251113_xxxxxx_1.wav"},
		{"out of range falls back", "20251113_995959_1.wav", "20251113_995959_1.wav"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FormatTimeFromFilename(tt.filename); got != tt.want {
				t.Errorf("FormatTimeFromFilename(%q) = %q, want %q", tt.filename, got, tt.want)
			}
		})
	}
}

func TestRadioUIDFromFilename(t *testing.T) {
	tests := []struct {
		filename string
		want     int
		wantOK   bool
	}{
		{"20251113_200214_26522_DMR_CC_3_GROUP_TGT_1_SRC_1005.wav", 1005, true},
		{"20251113_200214_26522_DMR_SRC_.wav", 0, false},
		{"nodigits.wav", 0, false},
		{"files/20251113/20251113_200214_1_SRC_42.wav", 42, true},
	}
	for _, tt := range tests {
		got, ok := RadioUIDFromFilename(tt.filename)
		if got != tt.want || ok != tt.wantOK {
			t.Errorf("RadioUIDFromFilename(%q) = (%d, %v), want (%d, %v)",
				tt.filename, got, ok, tt.want, tt.wantOK)
		}
	}
}

func TestDateFromFilename(t *testing.T) {
	tests := []struct {
		filename string
		want     string
	}{
		{"20251113_200214_26522_DMR.wav", "20251113"},
		{"19991113_200214_1.wav", ""}, // year out of range
		{"20251313_200214_1.wav", ""}, // month out of range
		{"20251100_200214_1.wav", ""}, // day out of range
		{"2025111_200214_1.wav", ""},  // too short
		{"notadate_200214_1.wav", ""},
		{"20251113.wav", ""}, // no underscore
	}
	for _, tt := range tests {
		if got := DateFromFilename(tt.filename); got != tt.want {
			t.Errorf("DateFromFilename(%q) = %q, want %q", tt.filename, got, tt.want)
		}
	}
}

func TestFormatDuration(t *testing.T) {
	// These must match Python's str(timedelta(seconds=round(x, 2))), which is
	// what the database and UI carried before the port.
	tests := []struct {
		seconds float64
		want    string
	}{
		{0, "0:00:00"},
		{5, "0:00:05"},
		{5.5, "0:00:05.500000"},
		{65, "0:01:05"},
		{3661, "1:01:01"},
		{0.5, "0:00:00.500000"},
		{1.234, "0:00:01.230000"},
		{2.25, "0:00:02.250000"},
	}
	for _, tt := range tests {
		if got := FormatDuration(tt.seconds); got != tt.want {
			t.Errorf("FormatDuration(%v) = %q, want %q", tt.seconds, got, tt.want)
		}
	}
}

func TestFormatDateDisplay(t *testing.T) {
	if got := FormatDateDisplay("20251113"); got != "2025/11/13" {
		t.Errorf("FormatDateDisplay = %q, want 2025/11/13", got)
	}
	if got := FormatDateDisplay("bad"); got != "bad" {
		t.Errorf("FormatDateDisplay passthrough = %q, want bad", got)
	}
}
