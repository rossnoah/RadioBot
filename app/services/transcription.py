"""Transcription service using Deepgram API."""
import logging
import httpx
from deepgram import DeepgramClient

from app.models import save_transcript
from app.config import DEEPGRAM_API_KEY

logger = logging.getLogger(__name__)

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
    """Transcribe a file and save the result."""
    try:
        [response, transcript] = get_transcription(file_path)
        # Convert Pydantic model to JSON string
        json_response = response.model_dump_json()
        save_transcript(file_path, transcript, json_response)
        logger.info(f"Transcribed: {file_path}")
    except Exception as e:
        logger.error(f"Transcription failed for {file_path}: {e}", exc_info=True)
