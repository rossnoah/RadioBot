"""File processing service - core logic for handling new recordings."""
import os
import logging
from datetime import timedelta

from app.models import get_transcript
from app.services.transcription import save_transcription
from app.services.notifications import check_transcript_for_alerts
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
