# RadioBot

## Applications

- **Noise Complaints**: Monitor radio chatter to know when campus security or police are responding to noise complaints in your area
- **Party Planning**: Get advance warning if authorities are heading to your location so you can adjust volume or wrap things up
- **Situational Awareness**: Stay informed about what's happening around campus during late-night gatherings
- **Risk Management**: Receive real-time alerts when specific keywords (like your address or dorm name) are mentioned on emergency channels

A real-time radio monitoring and streaming system that captures, processes, and broadcasts DMR radio transmissions through a web interface.

## Overview

This project provides a complete solution for monitoring digital radio communications. It captures DMR radio transmissions in real-time using SDR hardware, provides a browser-based dashboard for monitoring live audio and viewing transcriptions, sends alerts via GroupMe or Discord when specific words are detected, identifies speakers by mapping radio IDs to known users, and keeps all connected clients synchronized with instant WebSocket notifications.

## Features

- **Notifications**: Get alerts via GroupMe or Discord when specific words are detected in radio chatter
- **Modern Web Interface**: Dashboard for monitoring live audio and viewing transcriptions
- **Caller Tracking**: Identify speakers by mapping radio IDs to known users
- **Real-Time Updates**: Instant WebSocket notifications keep all connected clients ssynchronized

## Requirements

- RTL-SDR compatible hardware
- `dsd-fme` for digital signal decoding
- Python 3.x
- Dependencies listed in `requirements.txt`

## Installation

1. Install system dependencies (dsd-fme and RTL-SDR drivers)
2. Set up the virtual environment:
   ```bash
   source setup.sh
   ```
3. Install Python dependencies:
   ```bash
   pip install -r requirements.txt
   ```
4. Create and configure your `config.yaml`:
   ```bash
   cp config.yaml.example config.yaml
   ```
   Then edit `config.yaml` with your settings (see Configuration section below)
   - Set your web interface password
   - Configure radio settings (frequency and gain)
   - Add your Deepgram API key
   - Optionally configure notifications and unit mappings

## Configuration

All configuration is managed through the `config.yaml` file. Copy `config.yaml.example` to `config.yaml` and customize it for your setup.

### Configuration File Structure

The configuration file is organized into the following sections:

#### 1. Application Settings

**Required**: Core application settings

```yaml
application:
  # Password for accessing the web interface
  password: "your_secure_password_here"
```

- `password`: Required. Sets the password for the web dashboard login

#### 2. Radio Settings

**Required**: RTL-SDR radio receiver configuration

```yaml
radio:
  # Center frequency to tune the RTL-SDR receiver (in MHz)
  frequency: 461.375

  # RF gain setting (in dB)
  gain: -7

  # Optional: RTL-SDR device index (default: 0)
  device_index: 0

  # Optional: Sample rate in kHz (default: 32)
  sample_rate: 32
```

- `frequency`: Required. The center frequency to monitor in MHz (e.g., 461.375 for 461.375 MHz)
- `gain`: Required. RF gain in dB
  - Range: 0-49 dB (typical)
  - Use -7 for automatic gain control
  - Higher values increase sensitivity but may introduce noise
  - Recommended: -7 (auto) or 12-20 for manual control
- `device_index`: Optional. Which RTL-SDR device to use if you have multiple (default: 0)
- `sample_rate`: Optional. RTL-SDR sample rate in kHz (default: 32)

#### 3. API Keys and Credentials

**Required**: External service credentials

```yaml
apis:
  # Deepgram API key for audio transcription
  deepgram_api_key: "your_deepgram_api_key_here"
```

- `deepgram_api_key`: Required. Your Deepgram API key from https://deepgram.com
  - Used for converting radio audio recordings to text transcripts
  - Free tier available for testing

#### 4. Unit Mappings

**Optional**: Map radio unit IDs to friendly names

```yaml
units:
  1: "Dispatch"
  1001: "Unit 1: John Doe"
  1002: "Unit 2: Jane Smith"
  2001: "Cruiser 1"
```

- Map numeric radio IDs to human-readable names
- Units not in the mapping will display as "Unknown. Radio ID: {id}"
- Useful for identifying who is transmitting
- Examples:
  - `1`: "Dispatch" - Main dispatch center
  - `1001`: "Unit 1: John Doe" - Patrol unit with operator name
  - `2001`: "Cruiser 1" - Vehicle identifier
  - `4001`: "Shared Handheld" - Shared equipment

#### 5. Notification Configuration

**Optional**: Configure real-time alerts when keywords are detected

##### GroupMe Notifications

```yaml
notifications:
  groupme:
    enabled: true
    bot_id: "your_groupme_bot_id_here"
```

- `enabled`: Set to `true` to enable GroupMe notifications
- `bot_id`: Your GroupMe bot ID
  - Get from https://dev.groupme.com/bots
  - Create a bot for your group and copy its Bot ID
  - Leave empty or remove if not using GroupMe

##### Discord Notifications

```yaml
notifications:
  discord:
    enabled: false
    webhook_url: "https://discord.com/api/webhooks/your_webhook_url"
```

- `enabled`: Set to `true` to enable Discord notifications
- `webhook_url`: Your Discord webhook URL
  - Create in Discord: Server Settings → Integrations → Webhooks
  - Create webhook for desired channel and copy URL
  - Leave empty or remove if not using Discord

##### Alert Keywords

Configure which words/phrases trigger notifications:

```yaml
notifications:
  wordlists:
    # Standard wordlist: triggers alert on FIRST occurrence
    standard:
      words:
        - "noise complaint"
        - "your street name"
        - "your dorm building"

    # Strict wordlist: requires MULTIPLE occurrences
    strict:
      min_occurrences: 2
      words:
        - "party"
        - "disturbance"
```

**Standard Wordlist**:

- Triggers alert immediately on first occurrence
- Use for important words that should always generate alerts
- Examples: your address, building name, specific alert terms

**Strict Wordlist**:

- Requires word to appear multiple times in a single transmission
- Reduces false positives for common words
- `min_occurrences`: Set the threshold (default: 2)
- Examples: words like "party" or "noise" that might appear in casual conversation

### Configuration Examples

#### Minimal Setup (Required Only)

```yaml
application:
  password: "mySecurePassword123"

radio:
  frequency: 461.375
  gain: -7

apis:
  deepgram_api_key: "abc123def456..."

units:
  1: "Dispatch"

notifications:
  groupme:
    enabled: false
  discord:
    enabled: false
  wordlists:
    standard:
      words: []
    strict:
      min_occurrences: 2
      words: []
```

#### Full Setup with Notifications

```yaml
application:
  password: "mySecurePassword123"

radio:
  frequency: 461.375
  gain: 12
  device_index: 0
  sample_rate: 32

apis:
  deepgram_api_key: "abc123def456..."

units:
  1: "Dispatch"
  8301: "Unit 1: Officer Smith"
  8302: "Unit 2: Officer Jones"
  8401: "Campus Security"

notifications:
  groupme:
    enabled: true
    bot_id: "a1b2c3d4e5f6g7h8i9j0"

  discord:
    enabled: true
    webhook_url: "https://discord.com/api/webhooks/123/abc..."

  wordlists:
    standard:
      words:
        - "123 Main Street"
        - "Smith Hall"
        - "noise complaint"
        - "disturbance"

    strict:
      min_occurrences: 3
      words:
        - "party"
        - "gathering"
        - "loud"
```

### Security Notes

- **Never commit `config.yaml` to version control** - it contains sensitive credentials
- The file is already included in `.gitignore`
- `config.yaml.example` is safe to commit as it contains only placeholder values
- Use strong, unique passwords for `application.password`
- Keep your Deepgram API key secure and rotate if compromised
- Limit access to the config file on shared systems: `chmod 600 config.yaml`

## Usage

Start the server:

```bash
python server.py
```

The server will automatically:

1. Initialize the database
2. Start the file organizer thread
3. Launch the radio monitoring process with your configured settings
4. Start the web interface on port 4000

The radio process (dsd-fme) is automatically managed by the Python server. It will:

- Start automatically when the server starts
- Use the frequency and gain settings from your `config.yaml`
- Record audio files to the `./calls` directory
- Log DMR activity to `dmr_log.jsonl`
- Stop gracefully when the server shuts down

Access the web interface at `http://localhost:4000`

### Monitoring Radio Status

The server logs will show the radio status on startup:

```
Radio monitoring started: 461.375 MHz, gain: -7 dB
```

If the radio fails to start (e.g., dsd-fme not installed or RTL-SDR not connected), the server will continue running without radio monitoring and display an error message.

### Stopping the Server

Press `Ctrl+C` to stop the server. The radio process will be stopped automatically during shutdown.
