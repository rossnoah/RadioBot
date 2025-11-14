"""File system monitoring service."""
import os
import threading
import logging
from watchdog.observers import Observer
from watchdog.events import FileSystemEventHandler

from app.models import list_transcripts_filenames
from app.services.file_processor import process_file, set_socketio as set_processor_socketio

logger = logging.getLogger(__name__)

# File monitoring globals
observer_thread = None
transcript_thread = None
observer_lock = threading.Lock()
transcript_lock = threading.Lock()


def set_socketio(app_socketio):
    """Set the socketio instance for emitting events."""
    set_processor_socketio(app_socketio)


class FileChangeHandler(FileSystemEventHandler):
    """Handles file system events from watchdog."""

    def on_moved(self, event):
        """Handle file move events (new recordings)."""
        if not event.dest_path.endswith(".wav"):
            return

        logger.info(f"New recording: {event.dest_path}")
        # Process the file - delegate to the file_processor service
        process_file(event.dest_path, emit_event=True)


def start_watching_folder(record_folder: str):
    """Start watching the record folder for new files."""
    with observer_lock:
        try:
            event_handler = FileChangeHandler()
            observer = Observer()
            observer.schedule(event_handler, path=record_folder, recursive=True)
            observer.start()
            logger.info(f"Watching: {record_folder}")
            observer.join()
        except KeyboardInterrupt:
            observer.stop()
        except Exception as e:
            logger.error(f"File watching error: {e}", exc_info=True)
        finally:
            observer.join()


def create_missing_transcripts(record_folder: str):
    """Create transcripts for files missing from database."""
    from app.services.file_processor import process_file_batch

    with transcript_lock:
        try:
            logger.info("Checking for missing transcripts...")

            existing = set(list_transcripts_filenames())
            logger.info(f"Found {len(existing)} existing transcripts")

            files_to_transcribe = []
            for root, dirs, files in os.walk(record_folder):
                for file in files:
                    if file.endswith(".wav"):
                        full_path = os.path.join(root, file)
                        if full_path not in existing:
                            files_to_transcribe.append(full_path)

            logger.info(f"Found {len(files_to_transcribe)} files needing transcription")

            if files_to_transcribe:
                results = process_file_batch(files_to_transcribe)
                logger.info(f"Batch processing results: {results}")

        except Exception as e:
            logger.error(f"Transcript creation error: {e}", exc_info=True)


def start_observer_thread(record_folder: str):
    """Start file observer thread."""
    global observer_thread
    with observer_lock:
        if observer_thread is None or not observer_thread.is_alive():
            observer_thread = threading.Thread(
                target=start_watching_folder,
                args=(record_folder,),
                name="FileObserverThread",
                daemon=True
            )
            observer_thread.start()
            logger.info("Observer thread started")


def start_transcript_thread(record_folder: str):
    """Start transcript creation thread."""
    global transcript_thread
    with transcript_lock:
        if transcript_thread is None or not transcript_thread.is_alive():
            transcript_thread = threading.Thread(
                target=create_missing_transcripts,
                args=(record_folder,),
                name="TranscriptThread",
                daemon=True
            )
            transcript_thread.start()
            logger.info("Transcript thread started")
