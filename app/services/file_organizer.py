"""File organizer service - moves files from temp to organized date folders."""
import os
import shutil
import logging
import threading
import time
from pathlib import Path
from watchdog.observers import Observer
from watchdog.events import FileSystemEventHandler

from app.services.file_processor import process_file, set_socketio as set_processor_socketio

logger = logging.getLogger(__name__)

# Folder paths
TEMP_FOLDER = "temp"
FILES_FOLDER = "files"

# File monitoring globals
observer_thread = None
observer_lock = threading.Lock()

# Track recently processed files to prevent duplicates
processed_files = {}
processed_files_lock = threading.Lock()
PROCESSING_WINDOW = 2  # seconds to consider a file as "recently processed"


def set_socketio(app_socketio):
    """Set the socketio instance for emitting events."""
    set_processor_socketio(app_socketio)


def parse_date_from_filename(filename: str) -> str | None:
    """
    Extract date from filename timestamp.

    Example: 20251113_200214_26522_DMR_CC_3_GROUP_TGT_1_SRC_1.wav
    Returns: 20251113

    Args:
        filename: The filename to parse

    Returns:
        Date string in YYYYMMDD format or None if invalid
    """
    try:
        # Split filename and get first part (timestamp)
        parts = filename.split("_")
        if len(parts) < 2:
            return None

        timestamp = parts[0]

        # Validate timestamp format (YYYYMMDD)
        if len(timestamp) != 8:
            return None

        # Basic validation: check if it's all digits
        if not timestamp.isdigit():
            return None

        # Validate year, month, day ranges
        year = int(timestamp[:4])
        month = int(timestamp[4:6])
        day = int(timestamp[6:8])

        if year < 2000 or year > 2100:
            return None
        if month < 1 or month > 12:
            return None
        if day < 1 or day > 31:
            return None

        return timestamp

    except (ValueError, IndexError) as e:
        logger.error(f"Error parsing date from filename {filename}: {e}")
        return None


def organize_file(file_path: str) -> bool:
    """
    Move file from temp to files/YYYYMMDD/ folder.

    Args:
        file_path: Full path to the file in temp folder

    Returns:
        True if file was moved successfully, False otherwise
    """
    try:
        # Check if file was recently processed to prevent duplicates
        with processed_files_lock:
            current_time = time.time()

            # Clean up old entries
            expired_files = [
                f for f, t in processed_files.items()
                if current_time - t > PROCESSING_WINDOW
            ]
            for f in expired_files:
                del processed_files[f]

            # Check if this file was recently processed
            if file_path in processed_files:
                logger.debug(f"Skipping duplicate processing of {file_path}")
                return False

            # Mark file as being processed
            processed_files[file_path] = current_time

        # Check if source file exists
        if not os.path.exists(file_path):
            logger.debug(f"File no longer exists (likely already moved): {file_path}")
            return False

        filename = os.path.basename(file_path)

        # Extract date from filename
        date_str = parse_date_from_filename(filename)
        if not date_str:
            logger.warning(f"Could not parse date from filename: {filename}")
            return False

        # Create target directory path
        target_dir = os.path.join(FILES_FOLDER, date_str)
        os.makedirs(target_dir, exist_ok=True)

        # Move file to target directory
        target_path = os.path.join(target_dir, filename)

        # Check if file already exists
        if os.path.exists(target_path):
            logger.warning(f"File already exists at {target_path}, overwriting...")

        shutil.move(file_path, target_path)
        logger.info(f"Moved {filename} -> {target_dir}/")

        # Process the file after moving
        process_file(target_path, emit_event=True)

        return True

    except Exception as e:
        logger.error(f"Error organizing file {file_path}: {e}", exc_info=True)
        return False


class FileOrganizerHandler(FileSystemEventHandler):
    """Handles file system events for the temp folder."""

    def on_created(self, event):
        """Handle file creation events."""
        if event.is_directory:
            return

        if not event.src_path.endswith(".wav"):
            return

        logger.info(f"New file detected: {event.src_path}")
        organize_file(event.src_path)

    def on_moved(self, event):
        """Handle file move events (files moved into temp folder)."""
        if event.is_directory:
            return

        if not event.dest_path.endswith(".wav"):
            return

        logger.info(f"File moved into temp: {event.dest_path}")
        organize_file(event.dest_path)


def start_watching_temp():
    """Start watching the temp folder for new files."""
    with observer_lock:
        try:
            # Create temp and files folders if they don't exist
            os.makedirs(TEMP_FOLDER, exist_ok=True)
            os.makedirs(FILES_FOLDER, exist_ok=True)

            event_handler = FileOrganizerHandler()
            observer = Observer()
            observer.schedule(event_handler, path=TEMP_FOLDER, recursive=False)
            observer.start()
            logger.info(f"Watching temp folder: {TEMP_FOLDER}")
            observer.join()
        except KeyboardInterrupt:
            observer.stop()
        except Exception as e:
            logger.error(f"File watching error: {e}", exc_info=True)
        finally:
            observer.join()


def organize_existing_files():
    """Organize any existing files in temp folder on startup."""
    try:
        if not os.path.exists(TEMP_FOLDER):
            logger.info("Temp folder does not exist, skipping existing file organization")
            return

        files = [f for f in os.listdir(TEMP_FOLDER) if f.endswith(".wav")]

        if not files:
            logger.info("No existing files in temp folder")
            return

        logger.info(f"Found {len(files)} existing files in temp folder")

        success_count = 0
        for filename in files:
            file_path = os.path.join(TEMP_FOLDER, filename)
            if organize_file(file_path):
                success_count += 1

        logger.info(f"Organized {success_count}/{len(files)} existing files")

    except Exception as e:
        logger.error(f"Error organizing existing files: {e}", exc_info=True)


def start_organizer_thread():
    """Start file organizer thread."""
    global observer_thread
    with observer_lock:
        if observer_thread is None or not observer_thread.is_alive():
            # First organize existing files
            organize_existing_files()

            # Then start watching
            observer_thread = threading.Thread(
                target=start_watching_temp,
                name="FileOrganizerThread",
                daemon=True
            )
            observer_thread.start()
            logger.info("File organizer thread started")
