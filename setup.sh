#!/bin/bash
# Build RadioBot and install the on-device transcription fallback.
#
#   ./setup.sh              build, install libmoonshine, fetch the model
#   ./setup.sh --no-fallback  build only (Deepgram-only transcription)
#
# There is no Python here. Moonshine ships a C library, which the binary loads
# at runtime; this script just puts it in lib/ and pre-fetches the model so a
# Deepgram outage does not also mean waiting on a large download.
set -euo pipefail

cd "$(dirname "$0")"

MOONSHINE_VERSION="0.1.5"

echo "Building radiobot..."
go build -o radiobot ./cmd/radiobot
echo "Built ./radiobot"

if [ "${1:-}" = "--no-fallback" ]; then
    echo "Skipping the on-device fallback (--no-fallback)."
    exit 0
fi

# Pick the wheel matching this machine. The library is the only thing taken
# out of it.
case "$(uname -s)-$(uname -m)" in
    Linux-aarch64)  WHEEL_TAG="manylinux_2_34_aarch64"; LIB="libmoonshine.so" ;;
    Linux-x86_64)   WHEEL_TAG="manylinux_2_34_x86_64";  LIB="libmoonshine.so" ;;
    Darwin-arm64)   WHEEL_TAG="macosx_15_0_arm64";      LIB="libmoonshine.dylib" ;;
    *)
        echo "No prebuilt Moonshine library for $(uname -s)-$(uname -m)." >&2
        echo "Build radiobot with --no-fallback, or supply one via MOONSHINE_LIB." >&2
        exit 1
        ;;
esac

if [ ! -f "lib/$LIB" ]; then
    WHEEL="moonshine_voice-${MOONSHINE_VERSION}-py3-none-${WHEEL_TAG}.whl"
    URL="https://pypi.org/pypi/moonshine-voice/${MOONSHINE_VERSION}/json"

    echo "Fetching the Moonshine library for ${WHEEL_TAG}..."
    mkdir -p lib
    TMP="$(mktemp -d)"
    trap 'rm -rf "$TMP"' EXIT

    DOWNLOAD_URL="$(curl -sSL "$URL" | python3 -c "
import json, sys
print(next(f['url'] for f in json.load(sys.stdin)['urls'] if f['filename'] == '$WHEEL'))
")"
    curl -sSL -o "$TMP/wheel.zip" "$DOWNLOAD_URL"
    # A wheel is a zip; take the shared libraries and nothing else.
    (cd "$TMP" && unzip -qo wheel.zip 'moonshine_voice/*.so' 'moonshine_voice/*.dylib')
    cp "$TMP"/moonshine_voice/*."${LIB##*.}" lib/
    echo "Installed lib/$LIB"
else
    echo "lib/$LIB is already present."
fi

echo "Fetching the transcription model..."
./radiobot fetch-model

echo
echo "Done. Next:"
echo "  cp config.yaml.example config.yaml   # then edit it"
echo "  ./radiobot"
