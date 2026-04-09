"""Transcription service using Moonshine AI (on-device)."""
import logging
import os

logger = logging.getLogger(__name__)

# Lazy-loaded globals
_transcriber = None
_model_path = None
_model_arch = None


def _get_transcriber():
    """Get or create the Moonshine transcriber (lazy init)."""
    global _transcriber, _model_path, _model_arch
    if _transcriber is not None:
        return _transcriber

    from moonshine_voice import Transcriber, download_model

    if _model_path is None:
        logger.info("Downloading Moonshine Medium Streaming model for English...")
        _model_path, _model_arch = download_model(language="en", model_arch=None)
        logger.info(f"Moonshine model ready at {_model_path} (arch={_model_arch})")

    _transcriber = Transcriber(model_path=_model_path, model_arch=_model_arch)
    return _transcriber


def get_moonshine_transcription(file_path):
    """Transcribe a WAV file using Moonshine AI.

    Returns the transcript text, or empty string on failure.
    """
    try:
        from moonshine_voice import load_wav_file

        transcriber = _get_transcriber()
        audio_data, sample_rate = load_wav_file(file_path)

        transcript = transcriber.transcribe_without_streaming(audio_data, sample_rate)

        # Extract text from all lines
        lines = []
        for line in transcript.lines:
            if line.text.strip():
                lines.append(line.text.strip())

        return " ".join(lines)
    except Exception as e:
        logger.error(f"Moonshine transcription failed for {file_path}: {e}", exc_info=True)
        return ""
