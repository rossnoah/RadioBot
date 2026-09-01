#!/bin/bash
# Build the RadioBot server and set up the Moonshine transcription fallback.
#
#   ./setup.sh              build the binary and the Moonshine venv
#   ./setup.sh --no-python  build the binary only (Deepgram-only transcription)
set -euo pipefail

cd "$(dirname "$0")"

echo "Building radiobot..."
go build -o radiobot ./cmd/radiobot
echo "Built ./radiobot"

if [ "${1:-}" = "--no-python" ]; then
    echo "Skipping the Moonshine fallback (--no-python)."
    exit 0
fi

# Moonshine is a Python on-device speech model with no Go equivalent, so the
# fallback runs as a sidecar out of this venv. The server finds it at
# venv/bin/python3; override with RADIOBOT_PYTHON.
if [ ! -d "venv" ]; then
    echo "Creating venv for the Moonshine fallback..."
    python3 -m venv venv
fi

echo "Installing the Moonshine sidecar dependencies..."
venv/bin/pip install --quiet --upgrade pip
venv/bin/pip install --quiet -r requirements.txt

echo
echo "Done. Next:"
echo "  cp config.yaml.example config.yaml   # then edit it"
echo "  ./radiobot"
