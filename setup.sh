#!/bin/bash
# Build RadioBot as a single self-contained binary.
#
#   ./setup.sh                       build for this machine
#   ./setup.sh linux/arm64           cross-build for a Raspberry Pi
#   ./setup.sh --no-fallback         build without on-device transcription
#
# The Moonshine libraries are compiled into the binary, so a deployment is one
# file. Nothing here needs Python: the libraries are published inside wheels,
# and a wheel is a zip.
set -euo pipefail

cd "$(dirname "$0")"

if [ "${1:-}" = "--no-fallback" ]; then
    echo "Building without on-device transcription..."
    go build -o radiobot ./cmd/radiobot
    echo "Built ./radiobot (Deepgram-only)"
    exit 0
fi

TARGET="${1:-$(go env GOOS)/$(go env GOARCH)}"
GOOS_TARGET="${TARGET%/*}"
GOARCH_TARGET="${TARGET#*/}"

echo "Staging the Moonshine libraries for $TARGET..."
go run ./tools/fetch-moonshine -os "$GOOS_TARGET" -arch "$GOARCH_TARGET"

echo "Building radiobot for $TARGET..."
CGO_ENABLED=0 GOOS="$GOOS_TARGET" GOARCH="$GOARCH_TARGET" \
    go build -tags moonshine_embed -o radiobot ./cmd/radiobot

echo "Built ./radiobot ($(du -h radiobot | cut -f1), self-contained)"
echo
if [ "$TARGET" = "$(go env GOOS)/$(go env GOARCH)" ]; then
    echo "Next:"
    echo "  cp config.yaml.example config.yaml   # then edit it"
    echo "  ./radiobot fetch-model               # optional: pre-fetch the model"
    echo "  ./radiobot"
else
    echo "Copy ./radiobot and config.yaml to the device. Nothing else is needed;"
    echo "run './radiobot fetch-model' there to pre-fetch the transcription model."
fi
