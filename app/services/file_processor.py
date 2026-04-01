"""File processing service - core logic for handling new recordings."""
import os
import wave
import random
import logging
from datetime import datetime, timedelta

from app.models import get_transcript, save_transcript
from app.services.transcription import save_transcription
from app.services.notifications import check_transcript_for_alerts
from app.services.radio_manager import record_radio_message
from app.utils import (
    parse_time_from_filename,
    extract_radio_uid_from_filename,
    get_wav_length,
    get_unit_info,
)

logger = logging.getLogger(__name__)

# Global socketio instance
_socketio = None


def set_socketio(socketio_instance):
    """Set the socketio instance for emitting events."""
    global _socketio
    _socketio = socketio_instance


def get_file_data(file_path: str) -> dict | None:
    """
    Extract metadata from a file without saving to DB or sending notifications.

    This is the core data extraction logic that can be called from CLI, RPC, etc.

    Args:
        file_path: Full path to the WAV file

    Returns:
        Dict with file metadata or None if file is too short/invalid
    """
    if not file_path.endswith(".wav"):
        logger.warning(f"Skipping non-WAV file: {file_path}")
        return None

    # Check file length
    file_length = get_wav_length(file_path)
    if file_length < 0.5:
        logger.info(f"File too short ({file_length}s): {file_path}")
        return None

    try:
        # Extract file details
        filename = os.path.basename(file_path)
        folder_name = os.path.basename(os.path.dirname(file_path))
        formatted_time = parse_time_from_filename(filename)
        duration = str(timedelta(seconds=round(file_length, 2)))

        # Extract unit name
        radio_uid = extract_radio_uid_from_filename(filename)
        unit_name = get_unit_info(radio_uid) if radio_uid else None

        return {
            'file_path': file_path,
            'filename': filename,
            'folder_name': folder_name,
            'formatted_time': formatted_time,
            'duration': duration,
            'file_length': file_length,
            'radio_uid': radio_uid,
            'unit_name': unit_name,
        }

    except Exception as e:
        logger.error(f"Error extracting data from {file_path}: {e}", exc_info=True)
        return None


def process_file(file_path: str, emit_event: bool = True) -> bool:
    """
    Process a new recording file.

    Steps:
    1. Transcribe the file using Deepgram
    2. Save transcript to database
    3. Emit WebSocket event (if enabled)
    4. Check transcript for alert keywords

    Args:
        file_path: Full path to the WAV file
        emit_event: Whether to emit WebSocket event (set to False for batch processing)

    Returns:
        True if processing succeeded, False otherwise
    """
    # Get file data
    file_data = get_file_data(file_path)
    if not file_data:
        return False

    logger.info(f"Processing: {file_data['filename']}")

    # Record that we received a message
    try:
        record_radio_message()
    except Exception:
        pass  # Don't let status tracking break file processing

    # Step 1: Transcribe the file
    save_transcription(file_path)

    # Step 2: Get the transcript from DB
    transcript = get_transcript(file_path)

    # Step 3: Emit WebSocket event (optional)
    if emit_event and _socketio:
        try:
            _socketio.emit('file_added', {
                'filename': file_data['filename'],
                'formatted_time': file_data['formatted_time'],
                'duration': file_data['duration'],
                'transcript': transcript,
                'folder_name': file_data['folder_name'],
                'unit_name': file_data['unit_name']
            })
        except Exception as e:
            logger.error(f"Error emitting WebSocket event: {e}", exc_info=True)

    # Step 4: Check for alerts
    if transcript:
        try:
            check_transcript_for_alerts(transcript, file_data['unit_name'])
        except Exception as e:
            logger.error(f"Error checking transcript for alerts: {e}", exc_info=True)

    return True


def inject_fake_message(transcript: str, unit_id: int, unit_name: str, record_folder: str, duration: float = None) -> dict | None:
    """
    Inject a fake radio message without transcription.

    Creates a silent WAV file, saves the provided transcript directly to the DB,
    emits a WebSocket event, and runs alert checks — just like a real transmission.

    Args:
        transcript: The fake transcript text to use
        unit_id: Radio unit ID to embed in the filename
        unit_name: Display name for the unit (used in alerts and WebSocket event)
        record_folder: Root folder where date-organized recordings are stored
        duration: Audio duration in seconds; auto-calculated from word count if None

    Returns:
        Dict with filename and date, or None on failure
    """
    now = datetime.now()
    date_str = now.strftime("%Y%m%d")
    time_str = now.strftime("%H%M%S")
    random_id = random.randint(10000, 99999)
    filename = f"{date_str}_{time_str}_{random_id}_DMR_CC_1_GROUP_TGT_1_SRC_{unit_id}.wav"

    # Auto-calculate duration: ~2.5 words/sec for radio speech, minimum 2s
    if duration is None:
        word_count = len(transcript.split())
        duration = max(2.0, word_count / 2.5)

    # Ensure date folder exists
    date_folder = os.path.join(record_folder, date_str)
    os.makedirs(date_folder, exist_ok=True)

    file_path = os.path.join(date_folder, filename)

    # Write silent WAV (8kHz mono 16-bit PCM)
    sample_rate = 8000
    n_frames = int(sample_rate * duration)
    try:
        with wave.open(file_path, 'w') as wav_file:
            wav_file.setnchannels(1)
            wav_file.setsampwidth(2)
            wav_file.setframerate(sample_rate)
            wav_file.writeframes(b'\x00\x00' * n_frames)
    except Exception as e:
        logger.error(f"Failed to create fake WAV file: {e}", exc_info=True)
        return None

    # Save transcript directly to DB (no Deepgram), flagged as fake
    save_transcript(file_path, transcript, '{}', is_fake=True)

    # Emit WebSocket event so the UI updates in real time
    formatted_time = parse_time_from_filename(filename)
    duration_str = str(timedelta(seconds=round(duration, 2)))
    if _socketio:
        try:
            _socketio.emit('file_added', {
                'filename': filename,
                'formatted_time': formatted_time,
                'duration': duration_str,
                'transcript': transcript,
                'folder_name': date_str,
                'unit_name': unit_name,
            })
        except Exception as e:
            logger.error(f"Error emitting fake message WebSocket event: {e}", exc_info=True)

    # Run normal alert checks
    try:
        check_transcript_for_alerts(transcript, unit_name)
    except Exception as e:
        logger.error(f"Error checking fake transcript for alerts: {e}", exc_info=True)

    logger.info(f"Injected fake message: {filename}")
    return {'filename': filename, 'date': date_str}


def process_file_batch(file_paths: list) -> dict:
    """
    Process multiple files in batch mode (no WebSocket events).

    Args:
        file_paths: List of file paths to process

    Returns:
        Dict with success/failure counts
    """
    results = {
        'total': len(file_paths),
        'success': 0,
        'failed': 0,
        'skipped': 0,
    }

    for file_path in file_paths:
        if get_file_data(file_path) is None:
            results['skipped'] += 1
            continue

        if process_file(file_path, emit_event=False):
            results['success'] += 1
        else:
            results['failed'] += 1

    return results
