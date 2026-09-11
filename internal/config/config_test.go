package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const validConfig = `config_version: 2
application:
  password: "secret"
  branding: "Test Radio"
radio:
  frequency: 461.375
  gain: 32
apis:
  deepgram_api_key: "key"
`

func writeConfig(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadValid(t *testing.T) {
	cfg, err := Load(writeConfig(t, validConfig))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Application.Password != "secret" {
		t.Errorf("password = %q", cfg.Application.Password)
	}
	if cfg.Radio.FrequencyString() != "461.375" {
		t.Errorf("frequency = %q, want 461.375", cfg.Radio.FrequencyString())
	}
	if *cfg.Radio.Gain != 32 {
		t.Errorf("gain = %d, want 32", *cfg.Radio.Gain)
	}
	// Defaults the Python version applied at import time.
	if cfg.Application.TestPassword != "gotcha" {
		t.Errorf("test_password default = %q, want gotcha", cfg.Application.TestPassword)
	}
	if cfg.Notifications.Wordlists.Strict.MinOccurrences != 2 {
		t.Errorf("min_occurrences default = %d, want 2", cfg.Notifications.Wordlists.Strict.MinOccurrences)
	}
	if cfg.Backup.ScanIntervalSeconds != 60 || cfg.Backup.DBSnapshotIntervalHours != 24 {
		t.Errorf("backup defaults = %d/%d, want 60/24",
			cfg.Backup.ScanIntervalSeconds, cfg.Backup.DBSnapshotIntervalHours)
	}
}

// TestGainZeroIsValid guards the distinction between "gain: 0" and an absent
// gain, which is why Radio.Gain is a pointer.
func TestGainZeroIsValid(t *testing.T) {
	cfg, err := Load(writeConfig(t, strings.Replace(validConfig, "gain: 32", "gain: 0", 1)))
	if err != nil {
		t.Fatalf("Load with gain 0: %v", err)
	}
	if *cfg.Radio.Gain != 0 {
		t.Errorf("gain = %d, want 0", *cfg.Radio.Gain)
	}
}

func TestLoadRejectsMissingFields(t *testing.T) {
	tests := []struct {
		name    string
		config  string
		wantErr string
	}{
		{"no password", strings.Replace(validConfig, `  password: "secret"`, "", 1), "application.password"},
		{"no api key", strings.Replace(validConfig, `  deepgram_api_key: "key"`, "", 1), "deepgram_api_key"},
		{"no frequency", strings.Replace(validConfig, "  frequency: 461.375", "", 1), "radio.frequency"},
		{"no gain", strings.Replace(validConfig, "  gain: 32", "", 1), "radio.gain"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(writeConfig(t, tt.config))
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("Load error = %v, want one mentioning %q", err, tt.wantErr)
			}
		})
	}
}

func TestDeepgramKeyterms(t *testing.T) {
	cfg, err := Load(writeConfig(t, validConfig+`  deepgram_keyterms:
    - "Smith Hall"
    - "  ten four "
`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := []string{"Smith Hall", "ten four"}
	if !reflect.DeepEqual(cfg.APIs.DeepgramKeyterms, want) {
		t.Errorf("keyterms = %q, want %q", cfg.APIs.DeepgramKeyterms, want)
	}
}

func TestDeepgramKeytermsRejectsBlankAndOversized(t *testing.T) {
	_, err := Load(writeConfig(t, validConfig+"  deepgram_keyterms: [\"Smith Hall\", \"  \"]\n"))
	if err == nil || !strings.Contains(err.Error(), "empty entry") {
		t.Errorf("blank term: Load error = %v, want one mentioning an empty entry", err)
	}

	many := validConfig + "  deepgram_keyterms:\n" + strings.Repeat("    - term\n", MaxDeepgramKeyterms+1)
	_, err = Load(writeConfig(t, many))
	if err == nil || !strings.Contains(err.Error(), "at most") {
		t.Errorf("too many terms: Load error = %v, want one mentioning the limit", err)
	}
}

func TestLoadMissingFile(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "absent.yaml"))
	if err == nil || !strings.Contains(err.Error(), "config.yaml.example") {
		t.Errorf("Load error = %v, want guidance to copy config.yaml.example", err)
	}
}

func TestUnitName(t *testing.T) {
	cfg := &Config{Units: map[int]string{1001: "Dispatch"}}
	if got := cfg.UnitName(1001); got != "Dispatch" {
		t.Errorf("UnitName(1001) = %q", got)
	}
	if got := cfg.UnitName(7); got != "Unknown. Radio ID: 7" {
		t.Errorf("UnitName(7) = %q", got)
	}
}

// TestMigrateV1ToV2 covers the prank_password -> test_password rename, and
// checks that the on-disk file is rewritten and the original backed up.
func TestMigrateV1ToV2(t *testing.T) {
	v1 := `# A hand-written comment that should survive.
application:
  password: "secret"
  prank_password: "gotcha-old"
radio:
  frequency: 461.375
  gain: 32
apis:
  deepgram_api_key: "key"
`
	path := writeConfig(t, v1)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Application.TestPassword != "gotcha-old" {
		t.Errorf("test_password = %q, want gotcha-old", cfg.Application.TestPassword)
	}

	rewritten, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(rewritten), "prank_password") {
		t.Error("rewritten config still contains prank_password")
	}
	if !strings.HasPrefix(string(rewritten), "config_version: 2") {
		t.Errorf("config_version is not the first key:\n%s", rewritten)
	}
	// Unlike the Python version, the yaml.Node rewrite keeps comments.
	if !strings.Contains(string(rewritten), "hand-written comment") {
		t.Errorf("rewrite lost the file's comments:\n%s", rewritten)
	}

	backup, err := os.ReadFile(path + ".bak.v1")
	if err != nil {
		t.Fatalf("reading backup: %v", err)
	}
	if !strings.Contains(string(backup), "prank_password") {
		t.Error("backup does not contain the pre-migration config")
	}
}

// TestMigrateIsIdempotent checks that a second load neither rewrites nor
// re-backs-up an already-current config.
func TestMigrateIsIdempotent(t *testing.T) {
	path := writeConfig(t, validConfig)
	if _, err := Load(path); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != validConfig {
		t.Errorf("current config was rewritten:\n%s", after)
	}
	if _, err := os.Stat(path + ".bak.v2"); !os.IsNotExist(err) {
		t.Error("a backup was written for an already-current config")
	}
}

// TestMigrateNewerConfigIsLeftAlone covers a config written by a future build.
func TestMigrateNewerConfigIsLeftAlone(t *testing.T) {
	future := strings.Replace(validConfig, "config_version: 2", "config_version: 99", 1)
	path := writeConfig(t, future)
	if _, err := Load(path); err != nil {
		t.Fatalf("Load: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != future {
		t.Errorf("a newer config was rewritten:\n%s", after)
	}
}

func TestLoadRejectsInvalidYAML(t *testing.T) {
	_, err := Load(writeConfig(t, "application: [unclosed\n"))
	if err == nil {
		t.Error("Load accepted invalid YAML")
	}
}

// TestUnitsParseIntegerKeys guards the int-keyed units map, which YAML gives
// as strings unless the target type asks for ints.
func TestUnitsParseIntegerKeys(t *testing.T) {
	withUnits := validConfig + `units:
  1001: "Unit 1"
  2002: "Cruiser 2"
`
	cfg, err := Load(writeConfig(t, withUnits))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Units[1001] != "Unit 1" || cfg.Units[2002] != "Cruiser 2" {
		t.Errorf("units = %v", cfg.Units)
	}
}

// TestFrequencyStringTrimsZeros keeps the dsd-fme input spec and the UI from
// showing 461.375000000.
func TestFrequencyStringTrimsZeros(t *testing.T) {
	var node yaml.Node
	if err := yaml.Unmarshal([]byte("frequency: 461.5\ngain: 1\n"), &node); err != nil {
		t.Fatal(err)
	}
	var r Radio
	if err := node.Decode(&r); err != nil {
		t.Fatal(err)
	}
	if got := r.FrequencyString(); got != "461.5" {
		t.Errorf("FrequencyString = %q, want 461.5", got)
	}
}
