#!/usr/bin/env python3
"""Moonshine transcription sidecar.

Moonshine is a Python on-device speech model with no Go equivalent, so the Go
server shells out to this script when Deepgram is unavailable. It takes one
WAV path, prints the transcript to stdout, and puts diagnostics on stderr so
they never contaminate the result.

Exit codes: 0 on success (stdout may be empty for silence), 1 on failure.

Install with:  venv/bin/pip install "moonshine-voice>=0.0.52"
"""
import sys


def main() -> int:
    if len(sys.argv) != 2:
        print(f"usage: {sys.argv[0]} <file.wav>", file=sys.stderr)
        return 1

    file_path = sys.argv[1]

    try:
        from moonshine_voice import ModelArch, get_model_for_language, load_wav_file
        from moonshine_voice.transcriber import Transcriber
    except ImportError as e:
        print(f"moonshine-voice is not installed: {e}", file=sys.stderr)
        return 1

    try:
        # The model is cached on disk after the first download, so this is only
        # slow the very first time the fallback is used.
        print("Loading Moonshine Medium Streaming model for English...", file=sys.stderr)
        model_path, model_arch = get_model_for_language("en", ModelArch.MEDIUM_STREAMING)
        transcriber = Transcriber(model_path=model_path, model_arch=model_arch)

        audio_data, sample_rate = load_wav_file(file_path)
        transcript = transcriber.transcribe_without_streaming(audio_data, sample_rate)
    except Exception as e:
        print(f"transcription failed for {file_path}: {e}", file=sys.stderr)
        return 1

    lines = [line.text.strip() for line in transcript.lines if line.text.strip()]
    sys.stdout.write(" ".join(lines))
    return 0


if __name__ == "__main__":
    sys.exit(main())
