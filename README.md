# RadioBot

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
- Go 1.22 or newer (to build; the result is a single static binary)
- No Python. The on-device fallback is a C library loaded at runtime.

## Installation

1. Install system dependencies (dsd-fme and RTL-SDR drivers)
2. Build the server:
   ```bash
   ./setup.sh
   ```
   This produces a single self-contained `./radiobot` with the on-device
   transcription libraries compiled in. Pass `--no-fallback` for a smaller
   Deepgram-only build.
3. Create and configure your `config.yaml`:

   ```bash
   cp config.yaml.example config.yaml
   ```

   Then edit `config.yaml` with your settings (see Configuration section below)
   - Set your web interface password
   - Configure radio settings (frequency and gain)
   - Add your Deepgram API key
   - Optionally configure notifications and unit mappings

4. Install the service so it starts on boot and recovers on its own:

   ```bash
   sudo cp radiobot.service.example /etc/systemd/system/radiobot.service
   sudo systemctl enable --now radiobot
   ```

   The unit carries the recovery policy — see [Recovery](#recovery) below. No
   sudoers grant is needed: radiobot never reboots the machine itself.

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
```

- `frequency`: Required. The center frequency to monitor in MHz (e.g., 461.375 for 461.375 MHz)
- `gain`: Required. RF gain in dB
  - Range: 0-49 dB (typical)
  - Use -7 for automatic gain control
  - Higher values increase sensitivity but may introduce noise
  - Recommended: -7 (auto) or 12-20 for manual control
- `device_index`: Optional. Which RTL-SDR device to use if you have multiple (default: 0)

#### 3. API Keys and Credentials

**Required**: External service credentials

```yaml
apis:
  # Deepgram API key for audio transcription
  deepgram_api_key: "your_deepgram_api_key_here"

  # Optional: terms Deepgram should listen for
  deepgram_keyterms:
    - "Smith Hall"
    - "noise complaint"
```

- `deepgram_api_key`: Required. Your Deepgram API key from https://deepgram.com
  - Used for converting radio audio recordings to text transcripts
  - Free tier available for testing
- `deepgram_keyterms`: Optional. Names, places, callsigns, and jargon that
  Deepgram should recognize ([Keyterm Prompting](https://developers.deepgram.com/docs/keyterm))
  - Each list entry is one term; a multi-word entry is boosted as a single phrase
  - Capitalize proper nouns the way you want them to appear in transcripts
  - Up to 100 terms; 20-50 well-chosen ones work best
  - Only affects Deepgram, not the on-device Moonshine fallback

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
        - "Main Street"
        - "Second Street"

    # Strict wordlist: requires MULTIPLE occurrences
    strict:
      min_occurrences: 2
      words:
        - "example"
```

**Standard Wordlist**:

- Triggers alert immediately on first occurrence
- Use for important words that should always generate alerts

**Strict Wordlist**:

- Requires word to appear multiple times in a single transmission
- Reduces false positives for common words
- `min_occurrences`: Set the threshold (default: 2)

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

## Usage

Start the server:

```bash
./radiobot
```

Access the web interface at `http://localhost:4000`. Use `-addr` to listen
somewhere else, and `-config` to point at a different config file.

**When testing against a real `config.yaml`, pass `-notify console`:**

```bash
./radiobot -notify console
```

Alert keywords and escalations both send to GroupMe and Discord, so running
the app on a laptop — where `dsd-fme` is usually missing, and the supervisor
will escalate — posts real messages to real people. Console mode makes every
same decision and logs what it would have sent instead of sending it.

To transcribe and file a single WAV from an external script:

```bash
./radiobot ingest /path/to/recording.wav
```

### Building for the Raspberry Pi

Build the whole thing on your laptop and copy one file:

```bash
./setup.sh linux/arm64          # Pi 4/5, 64-bit
scp radiobot config.yaml pi:~/RadioBot/
```

`setup.sh` fetches the Moonshine libraries for the *target* platform, not the
build host, and compiles them into the binary. There is no cgo, so the
cross-compile is an ordinary `GOOS`/`GOARCH` build. Templates are embedded
too, so the binary is the deployment.

On first use the binary unpacks its libraries into a cache directory
(`$XDG_CACHE_HOME/radiobot`, or `RADIOBOT_CACHE_DIR`) and loads them from
there — a shared library has to be a file on disk for the loader to map it.
The directory is named after a digest of the libraries, so upgrading the
binary cannot collide with what an older one unpacked.

The transcription model is separate: it is ordinary data, identical on every
platform, and downloaded at runtime into `models/`. Fetch it ahead of time
with `./radiobot fetch-model`.

## Architecture

```
cmd/radiobot        entry point: wiring and graceful shutdown
internal/config     config.yaml loading, validation, versioned migrations
internal/db         SQLite store (transcripts, restarts, backup bookkeeping)
internal/radio      dsd-fme process supervision and the watchdog
internal/organizer  watches temp/, files recordings under files/YYYYMMDD/
internal/processor  the recording pipeline: transcribe, store, broadcast, alert
internal/transcribe Deepgram client and the Moonshine fallback state machine
internal/notify     GroupMe and Discord keyword alerts
internal/backup     S3 backup via presigned URLs
internal/hub        WebSocket fan-out for live dashboard updates
internal/web        HTTP handlers and embedded templates
internal/moonshine  Go binding to the Moonshine C ABI (on-device fallback)
internal/sysinfo    host and Go runtime metrics for the status page
internal/systemd    sd_notify, for readiness and liveness
internal/escalation the give-up policy that keeps a fault from reboot-looping
```

### Recovery

Radio hardware fails in ways that need escalating responses, so recovery is a
ladder and each rung is owned by whoever implements it best.

1. **radiobot restarts dsd-fme.** The supervisor in `internal/radio` restarts
   the decoder with exponential backoff (2s doubling to 60s) when it exits,
   fails to start, or goes quiet for longer than `frozen_timeout_seconds`. A
   run that lasts five minutes resets the backoff.
2. **systemd restarts radiobot.** When five consecutive runs fail — or the
   RTL-SDR disappears from the USB bus, which a decoder restart cannot fix —
   radiobot exits non-zero. A fresh process gets a fresh libusb context, which
   sometimes fixes what a child restart cannot.
3. **systemd reboots the machine.** `StartLimitBurst` and
   `StartLimitAction=reboot` in the unit file handle this, including the rate
   limiting.

The one thing systemd cannot do is remember that the reboot did not help — its
start counter resets on boot. So radiobot records every handoff in the
`escalations` table, and once there have been three in two hours it stops
escalating altogether: the device stays up in **degraded mode**, keeps retrying
the radio every five minutes, shows a banner on `/status`, and sends a GroupMe
or Discord message saying it has given up. It announces itself again if the
radio comes back. A device that cannot fix itself should tell you, not
reboot-loop until someone drives out to it.

`WatchdogSec=120` in the unit catches the remaining case: radiobot itself
hanging. It pings systemd only while the radio supervisor is still cycling, so
a wedged process stops the pings and gets restarted.

To watch the ladder in action:

```bash
journalctl -u radiobot -f
```

### Transcription

Deepgram is the primary engine. If it fails three times in a row, the server
switches to Moonshine — an on-device model — and probes Deepgram every five
minutes until it recovers. The current engine is shown on the status page.

Moonshine is called through its C ABI, the same one its Python, Swift, and
Java bindings use. The library is `dlopen`ed at runtime rather than linked, so
the binary still builds with `CGO_ENABLED=0` and cross-compiles to the Pi from
anywhere.

- `-moonshine-lib` sets the library path, overriding the embedded copy
  (default: `$MOONSHINE_LIB`, then the embedded copy, then `lib/` beside the
  binary, then the system loader's search path).
- `-moonshine-models` sets the model directory (default `models/moonshine`).
- `radiobot fetch-model` downloads the model ahead of time. Do this during
  setup: the model is a few hundred megabytes, and the alternative is fetching
  it during a Deepgram outage.

The struct layouts this binding reads are an ABI contract, so the library's
version is checked on load and a mismatch refuses to run rather than reading
native memory at the wrong offsets.

## S3 Backup (Optional)

Continuously backs up all recordings and a daily gzipped snapshot of `transcripts.db` to S3. The device never holds AWS credentials — it holds a shared secret and asks a small Lambda for presigned, size-bound upload URLs. The Lambda enforces a daily upload quota (count and bytes) in DynamoDB, so a leaked secret can at worst upload up to the quota until you rotate it; it can never read, list, or delete anything. Backup failures never affect radio capture: uploads run in a background goroutine and are retried on the next scan.

### Provisioning (once, from any machine with AWS access)

```bash
brew install opentofu   # or terraform
cd infra
tofu init
tofu apply
```

This creates: a private versioned S3 bucket with lifecycle rules, a DynamoDB quota table, the Lambda with a public Function URL, and a random device secret. Optional variables (see `infra/variables.tf`): quota limits and `budget_alert_email` for a monthly cost alert.

### Enabling on the device

Add to `config.yaml` using the apply outputs:

```yaml
backup:
  enabled: true
  endpoint_url: "<tofu output backup_endpoint_url>"
  secret: "<tofu output -raw device_secret>"
```

On first start with backup enabled, all existing recordings in `files/` are backfilled; after that, new recordings upload within one scan interval (default 60s). Upload state is tracked in the `backup_uploads` table in `transcripts.db`.

### Restoring

From any machine with AWS admin credentials:

```bash
aws s3 sync s3://radiobot-backup-<account-id>/radiobot/recordings/ files/
aws s3 cp s3://radiobot-backup-<account-id>/radiobot/db/transcripts.db.gz - | gunzip > transcripts.db
```

### Rotating the secret

```bash
cd infra
tofu apply -replace=random_password.device_secret
```

Then update `backup.secret` in `config.yaml` on the device.
