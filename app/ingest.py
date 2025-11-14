"""Ingest handler for external scripts to submit audio files."""
import json
import logging
from pathlib import Path
from datetime import datetime

from app.models import save_transcript
from app.services.file_processor import process_file, get_file_data

logger = logging.getLogger(__name__)


def ingest_file(file_path: str, metadata: dict = None, emit_event: bool = True) -> dict:
    """
    Ingest a WAV file from an external source (script, RPC, etc).

    Args:
        file_path: Path to the WAV file
        metadata: Optional dict with recording metadata (freq, talkgroup, src, etc.)
        emit_event: Whether to emit WebSocket event

    Returns:
        Dict with status and details
    """
    path = Path(file_path)

    if not path.exists():
        return {
            'success': False,
            'error': f'File not found: {file_path}',
        }

    # Only accept WAV files
    if path.suffix.lower() != ".wav":
        return {
            'success': False,
            'error': f'File must be WAV format: {file_path}',
        }

    logger.info(f"Ingesting file: {file_path}")

    # Get file data
    file_data = get_file_data(str(path))
    if not file_data:
        return {
            'success': False,
            'error': f'File validation failed: {file_path}',
        }

    # Process the file
    try:
        process_file(str(path), emit_event=emit_event)

        result = {
            'success': True,
            'filename': file_data['filename'],
            'duration': file_data['duration'],
            'file_length': file_data['file_length'],
            'unit_name': file_data['unit_name'],
            'metadata': metadata or {},
        }

        logger.info(f"✓ Successfully ingested: {file_path}")
        return result

    except Exception as e:
        logger.error(f"Failed to ingest {file_path}: {e}", exc_info=True)
        return {
            'success': False,
            'error': str(e),
        }


def ingest_from_json(file_path: str, json_metadata: str, emit_event: bool = True) -> dict:
    """
    Ingest a file with JSON metadata string.

    Args:
        file_path: Path to the audio file
        json_metadata: JSON string with recording metadata
        emit_event: Whether to emit WebSocket event

    Returns:
        Dict with status and details
    """
    try:
        metadata = json.loads(json_metadata)
    except json.JSONDecodeError as e:
        logger.error(f"Invalid JSON metadata: {e}")
        return {
            'success': False,
            'error': f'Invalid JSON: {e}',
        }

    return ingest_file(file_path, metadata, emit_event)


def ingest_from_json_file(file_path: str, json_file_path: str, emit_event: bool = True) -> dict:
    """
    Ingest a file with JSON metadata from a file.

    Args:
        file_path: Path to the audio file
        json_file_path: Path to JSON metadata file
        emit_event: Whether to emit WebSocket event

    Returns:
        Dict with status and details
    """
    json_path = Path(json_file_path)

    if not json_path.exists():
        logger.error(f"JSON file not found: {json_file_path}")
        return {
            'success': False,
            'error': f'JSON file not found: {json_file_path}',
        }

    try:
        with open(json_path) as f:
            metadata = json.load(f)
    except json.JSONDecodeError as e:
        logger.error(f"Invalid JSON in {json_file_path}: {e}")
        return {
            'success': False,
            'error': f'Invalid JSON in file: {e}',
        }

    return ingest_file(file_path, metadata, emit_event)
