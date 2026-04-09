"""Transcription service using Deepgram API with Moonshine AI fallback."""
import time
import logging
from datetime import datetime
import httpx
from deepgram import DeepgramClient

from app.models import save_transcript
from app.config import DEEPGRAM_API_KEY

logger = logging.getLogger(__name__)

MAX_RETRIES = 3
RETRY_DELAY = 5  # seconds
DEEPGRAM_RETRY_INTERVAL = 300  # seconds (5 minutes) between Deepgram retries while in fallback

# Deepgram client for transcription with custom timeout
deepgram = DeepgramClient(
    api_key=DEEPGRAM_API_KEY,
    httpx_client=httpx.Client(
        timeout=httpx.Timeout(300.0, connect=10.0)
    )
)

# Fallback state
_using_fallback = False
_fallback_since = None
_last_deepgram_retry = None


def get_transcription_status():
    """Return current transcription engine status for display."""
    if _using_fallback:
        return {
            "engine": "moonshine",
            "fallback_since": _fallback_since,
        }
    return {
        "engine": "deepgram",
        "fallback_since": None,
    }


def _try_deepgram(file_path):
    """Attempt Deepgram transcription. Returns (response, transcript) or raises."""
    with open(file_path, "rb") as file:
        buffer_data = file.read()

    response = deepgram.listen.v1.media.transcribe_file(
        request=buffer_data,
        model="nova-3",
        smart_format=True
    )

    transcript = response.results.channels[0].alternatives[0].transcript
    return response, transcript


def _try_moonshine(file_path):
    """Attempt Moonshine transcription. Returns transcript text."""
    from app.services.moonshine_transcription import get_moonshine_transcription
    return get_moonshine_transcription(file_path)


def save_transcription(file_path, duration=None):
    """Transcribe a file and save the result. Falls back to Moonshine if Deepgram fails."""
    global _using_fallback, _fallback_since, _last_deepgram_retry

    # If in fallback mode, check whether to retry Deepgram
    if _using_fallback:
        now = time.time()
        should_retry_deepgram = (
            _last_deepgram_retry is None or
            (now - _last_deepgram_retry) >= DEEPGRAM_RETRY_INTERVAL
        )

        if should_retry_deepgram:
            _last_deepgram_retry = now
            try:
                response, transcript = _try_deepgram(file_path)
                # Deepgram recovered
                _using_fallback = False
                _fallback_since = None
                json_response = response.model_dump_json()
                save_transcript(file_path, transcript, json_response, duration=duration)
                logger.info(f"Deepgram recovered. Transcribed: {file_path}")
                return
            except Exception as e:
                logger.warning(f"Deepgram still failing during retry: {e}")

        # Use Moonshine
        transcript = _try_moonshine(file_path)
        save_transcript(file_path, transcript, '{"engine": "moonshine"}', duration=duration)
        logger.info(f"Transcribed (Moonshine fallback): {file_path}")
        return

    # Normal path: try Deepgram with retries
    for attempt in range(1, MAX_RETRIES + 1):
        try:
            response, transcript = _try_deepgram(file_path)
            json_response = response.model_dump_json()
            save_transcript(file_path, transcript, json_response, duration=duration)
            logger.info(f"Transcribed: {file_path}")
            return
        except Exception as e:
            if attempt < MAX_RETRIES:
                logger.warning(
                    f"Transcription attempt {attempt}/{MAX_RETRIES} failed for {file_path}: {e}, "
                    f"retrying in {RETRY_DELAY}s..."
                )
                time.sleep(RETRY_DELAY)
            else:
                logger.error(
                    f"Deepgram failed after {MAX_RETRIES} attempts for {file_path}: {e}",
                    exc_info=True
                )

    # All Deepgram retries exhausted — switch to fallback
    _using_fallback = True
    _fallback_since = datetime.now().strftime("%Y-%m-%d %H:%M:%S")
    _last_deepgram_retry = time.time()
    logger.warning(f"Switching to Moonshine fallback after Deepgram failures")

    transcript = _try_moonshine(file_path)
    save_transcript(file_path, transcript, '{"engine": "moonshine"}', duration=duration)
    logger.info(f"Transcribed (Moonshine fallback): {file_path}")
