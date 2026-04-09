"""Transcription service using Deepgram API."""
import time
import logging
import httpx
from deepgram import DeepgramClient

from app.models import save_transcript
from app.config import DEEPGRAM_API_KEY

logger = logging.getLogger(__name__)

MAX_RETRIES = 3
RETRY_DELAY = 5  # seconds

# Deepgram client for transcription with custom timeout
deepgram = DeepgramClient(
    api_key=DEEPGRAM_API_KEY,
    httpx_client=httpx.Client(
        timeout=httpx.Timeout(300.0, connect=10.0)
    )
)


def get_transcription(file_path):
    """Get transcription from Deepgram API."""
    with open(file_path, "rb") as file:
        buffer_data = file.read()

    response = deepgram.listen.v1.media.transcribe_file(
        request=buffer_data,
        model="nova-3",
        smart_format=True
    )

    # Access response attributes (not dictionary keys)
    transcript = response.results.channels[0].alternatives[0].transcript
    return [response, transcript]


def save_transcription(file_path):
    """Transcribe a file and save the result, with retries on failure."""
    last_error = None
    for attempt in range(1, MAX_RETRIES + 1):
        try:
            [response, transcript] = get_transcription(file_path)
            json_response = response.model_dump_json()
            save_transcript(file_path, transcript, json_response)
            logger.info(f"Transcribed: {file_path}")
            return
        except Exception as e:
            last_error = e
            if attempt < MAX_RETRIES:
                logger.warning(
                    f"Transcription attempt {attempt}/{MAX_RETRIES} failed for {file_path}: {e}, "
                    f"retrying in {RETRY_DELAY}s..."
                )
                time.sleep(RETRY_DELAY)
            else:
                logger.error(
                    f"Transcription failed after {MAX_RETRIES} attempts for {file_path}: {e}",
                    exc_info=True
                )
