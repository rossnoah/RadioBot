// Package config loads and validates config.yaml.
package config

import (
	"fmt"
	"os"
	"strconv"

	"gopkg.in/yaml.v3"
)

// Config mirrors the structure of config.yaml.
type Config struct {
	ConfigVersion int            `yaml:"config_version"`
	Application   Application    `yaml:"application"`
	Radio         Radio          `yaml:"radio"`
	APIs          APIs           `yaml:"apis"`
	Units         map[int]string `yaml:"units"`
	Notifications Notifications  `yaml:"notifications"`
	Backup        Backup         `yaml:"backup"`
}

type Application struct {
	Password     string `yaml:"password"`
	Branding     string `yaml:"branding"`
	TestPassword string `yaml:"test_password"`
}

type Radio struct {
	Frequency   float64 `yaml:"frequency"`
	Gain        *int    `yaml:"gain"`
	DeviceIndex int     `yaml:"device_index"`
	PPM         int     `yaml:"ppm"`
}

type APIs struct {
	DeepgramAPIKey string `yaml:"deepgram_api_key"`
}

type Notifications struct {
	GroupMe   GroupMe   `yaml:"groupme"`
	Discord   Discord   `yaml:"discord"`
	Wordlists Wordlists `yaml:"wordlists"`
}

type GroupMe struct {
	Enabled bool   `yaml:"enabled"`
	BotID   string `yaml:"bot_id"`
}

type Discord struct {
	Enabled    bool   `yaml:"enabled"`
	WebhookURL string `yaml:"webhook_url"`
}

type Wordlists struct {
	Standard struct {
		Words []string `yaml:"words"`
	} `yaml:"standard"`
	Strict struct {
		Words          []string `yaml:"words"`
		MinOccurrences int      `yaml:"min_occurrences"`
	} `yaml:"strict"`
}

type Backup struct {
	Enabled                 bool   `yaml:"enabled"`
	EndpointURL             string `yaml:"endpoint_url"`
	Secret                  string `yaml:"secret"`
	ScanIntervalSeconds     int    `yaml:"scan_interval_seconds"`
	DBSnapshotIntervalHours int    `yaml:"db_snapshot_interval_hours"`
}

// Path is the location of the config file, relative to the working directory.
const Path = "config.yaml"

// Load reads config.yaml, applies any pending migrations, and validates the
// required fields. The Python version cached this in a module global; here the
// caller holds the single instance and passes it down.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("configuration file not found at %s: create config.yaml from config.yaml.example", path)
		}
		return nil, err
	}

	migrated, err := Migrate(data, path)
	if err != nil {
		return nil, err
	}

	var c Config
	if err := yaml.Unmarshal(migrated, &c); err != nil {
		return nil, fmt.Errorf("invalid YAML in %s: %w", path, err)
	}

	if err := c.validate(); err != nil {
		return nil, err
	}
	c.applyDefaults()
	return &c, nil
}

func (c *Config) validate() error {
	if c.Application.Password == "" {
		return fmt.Errorf("application.password is not set in config.yaml")
	}
	if c.APIs.DeepgramAPIKey == "" {
		return fmt.Errorf("apis.deepgram_api_key is not set in config.yaml")
	}
	if c.Radio.Frequency == 0 {
		return fmt.Errorf("radio.frequency is not set in config.yaml")
	}
	if c.Radio.Gain == nil {
		return fmt.Errorf("radio.gain is not set in config.yaml")
	}
	return nil
}

func (c *Config) applyDefaults() {
	if c.Application.Branding == "" {
		c.Application.Branding = "Radio Bot"
	}
	if c.Application.TestPassword == "" {
		c.Application.TestPassword = "gotcha"
	}
	if c.Notifications.Wordlists.Strict.MinOccurrences == 0 {
		c.Notifications.Wordlists.Strict.MinOccurrences = 2
	}
	if c.Backup.ScanIntervalSeconds == 0 {
		c.Backup.ScanIntervalSeconds = 60
	}
	if c.Backup.DBSnapshotIntervalHours == 0 {
		c.Backup.DBSnapshotIntervalHours = 24
	}
	if c.Units == nil {
		c.Units = map[int]string{}
	}
}

// UnitName maps a radio unit ID to its configured display name.
func (c *Config) UnitName(unitID int) string {
	if name, ok := c.Units[unitID]; ok {
		return name
	}
	return "Unknown. Radio ID: " + strconv.Itoa(unitID)
}

// FrequencyString renders the frequency the way dsd-fme and the UI expect,
// trimming the trailing zeros Go would otherwise print.
func (r Radio) FrequencyString() string {
	return strconv.FormatFloat(r.Frequency, 'f', -1, 64)
}
