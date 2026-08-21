"""Optional S3 backup service.

Uploads recordings, periodic database snapshots, and the config file to S3
via presigned URLs issued by the backup Lambda (see infra/). The device holds
only a shared secret, never AWS credentials.

Design constraints:
- Must never block or break primary operation: everything runs in a single
  daemon thread, every failure is caught and logged, and a failed upload is
  simply retried on a later scan.
- A periodic scanner (rather than a hook in the recording pipeline) handles
  backfill of historical files, new files, and retries with the same code path.
"""
import gzip
import logging
import os
import sqlite3
import threading
import time
from datetime import datetime

import requests

from app.config import get_config
from app.models import get_db_connection, DATABASE_FILE

logger = logging.getLogger(__name__)

FILES_FOLDER = "files"
CONFIG_FILE = "config.yaml"
DB_SNAPSHOT_MARKER = "__db_snapshot__"
CONFIG_BACKUP_MARKER = "__config_backup__"
REQUEST_TIMEOUT = 30
UPLOAD_TIMEOUT = 300

_backup_thread = None
_thread_lock = threading.Lock()


def get_backup_settings() -> dict | None:
    """Return backup settings if the feature is enabled and configured, else None."""
    backup = get_config().get("backup") or {}
    if not backup.get("enabled"):
        return None

    endpoint_url = backup.get("endpoint_url")
    secret = backup.get("secret")
    if not endpoint_url or not secret:
        logger.warning("Backup is enabled but endpoint_url/secret are not set; backup disabled")
        return None

    return {
        "endpoint_url": endpoint_url,
        "secret": secret,
        "scan_interval": int(backup.get("scan_interval_seconds", 60)),
        "db_snapshot_interval": int(backup.get("db_snapshot_interval_hours", 24)) * 3600,
    }


def _ensure_table():
    conn = get_db_connection()
    try:
        conn.execute('''
            CREATE TABLE IF NOT EXISTS backup_uploads (
                path TEXT PRIMARY KEY,
                size INTEGER,
                uploaded_at TEXT NOT NULL
            )
        ''')
        conn.commit()
    finally:
        conn.close()


def _get_uploaded_paths() -> set:
    conn = get_db_connection()
    try:
        cursor = conn.execute('SELECT path FROM backup_uploads')
        return {row['path'] for row in cursor.fetchall()}
    finally:
        conn.close()


def _mark_uploaded(path: str, size: int):
    conn = get_db_connection()
    try:
        conn.execute(
            'INSERT OR REPLACE INTO backup_uploads (path, size, uploaded_at) VALUES (?, ?, ?)',
            (path, size, datetime.now().strftime("%Y-%m-%d %H:%M:%S"))
        )
        conn.commit()
    finally:
        conn.close()


def _last_marker_time(marker: str) -> float:
    """Return when the given marker row was last uploaded, as a unix timestamp."""
    conn = get_db_connection()
    try:
        cursor = conn.execute(
            'SELECT uploaded_at FROM backup_uploads WHERE path = ?', (marker,)
        )
        row = cursor.fetchone()
        if not row:
            return 0.0
        return datetime.strptime(row['uploaded_at'], "%Y-%m-%d %H:%M:%S").timestamp()
    except Exception:
        return 0.0
    finally:
        conn.close()


class QuotaExceeded(Exception):
    """Raised when the backend reports the daily upload quota is exhausted."""


def _upload_file(settings: dict, local_path: str, remote_key: str, track: bool = True) -> bool:
    """Upload one file via a presigned POST. Returns True on success.

    Raises QuotaExceeded on a 429 so the scan loop can stop for this round.
    """
    size = os.path.getsize(local_path)

    resp = requests.post(
        settings["endpoint_url"],
        json={"key": remote_key, "size": size},
        headers={"x-backup-secret": settings["secret"]},
        timeout=REQUEST_TIMEOUT,
    )
    if resp.status_code == 429:
        raise QuotaExceeded()
    if resp.status_code != 200:
        logger.warning(f"Backup presign failed ({resp.status_code}) for {remote_key}: {resp.text[:200]}")
        return False

    presigned = resp.json()
    with open(local_path, 'rb') as f:
        upload = requests.post(
            presigned["url"],
            data=presigned["fields"],
            files={"file": (os.path.basename(local_path), f)},
            timeout=UPLOAD_TIMEOUT,
        )
    if upload.status_code not in (200, 201, 204):
        logger.warning(f"Backup upload failed ({upload.status_code}) for {remote_key}: {upload.text[:200]}")
        return False

    if track:
        _mark_uploaded(local_path, size)
    return True


def _scan_recordings(settings: dict) -> int:
    """Upload any recordings not yet backed up (historical and new). Returns count uploaded."""
    if not os.path.isdir(FILES_FOLDER):
        return 0

    uploaded = _get_uploaded_paths()
    pending = []
    for date_folder in sorted(os.listdir(FILES_FOLDER)):
        folder_path = os.path.join(FILES_FOLDER, date_folder)
        if not (os.path.isdir(folder_path) and len(date_folder) == 8 and date_folder.isdigit()):
            continue
        for filename in sorted(os.listdir(folder_path)):
            if not filename.endswith(".wav"):
                continue
            local_path = os.path.join(folder_path, filename)
            if local_path not in uploaded:
                pending.append((local_path, f"recordings/{date_folder}/{filename}"))

    if not pending:
        return 0

    logger.info(f"Backup: {len(pending)} recording(s) to upload")
    count = 0
    for local_path, remote_key in pending:
        try:
            if _upload_file(settings, local_path, remote_key):
                count += 1
        except QuotaExceeded:
            logger.warning("Backup: daily upload quota exceeded, pausing until next scan")
            break
        except Exception as e:
            logger.warning(f"Backup: error uploading {local_path}: {e}")
    if count:
        logger.info(f"Backup: uploaded {count} recording(s)")
    return count


def _snapshot_db(settings: dict):
    """Take a consistent SQLite snapshot, gzip it, and upload it.

    Uploads to a fixed key; bucket versioning preserves history.
    """
    if time.time() - _last_marker_time(DB_SNAPSHOT_MARKER) < settings["db_snapshot_interval"]:
        return

    os.makedirs("temp", exist_ok=True)
    snapshot_path = os.path.join("temp", "db_snapshot.db")
    gzip_path = snapshot_path + ".gz"
    try:
        # SQLite's backup API gives a consistent copy even mid-write (WAL mode).
        src = sqlite3.connect(DATABASE_FILE, timeout=10)
        dst = sqlite3.connect(snapshot_path)
        try:
            src.backup(dst)
        finally:
            dst.close()
            src.close()

        with open(snapshot_path, 'rb') as f_in, gzip.open(gzip_path, 'wb') as f_out:
            f_out.writelines(f_in)

        # track=False: snapshots recur on a fixed key, tracked via the marker row instead
        if _upload_file(settings, gzip_path, "db/transcripts.db.gz", track=False):
            _mark_uploaded(DB_SNAPSHOT_MARKER, os.path.getsize(gzip_path))
            logger.info("Backup: uploaded database snapshot")
    finally:
        for path in (snapshot_path, gzip_path):
            try:
                if os.path.exists(path):
                    os.remove(path)
            except OSError:
                pass


def _backup_config(settings: dict):
    """Upload config.yaml whenever it has changed since the last upload.

    Uploads to a fixed key; bucket versioning preserves history.
    """
    try:
        mtime = os.path.getmtime(CONFIG_FILE)
    except OSError:
        return

    if mtime <= _last_marker_time(CONFIG_BACKUP_MARKER):
        return

    if _upload_file(settings, CONFIG_FILE, "config/config.yaml", track=False):
        _mark_uploaded(CONFIG_BACKUP_MARKER, os.path.getsize(CONFIG_FILE))
        logger.info("Backup: uploaded config.yaml")


def _run_loop(settings: dict):
    consecutive_failures = 0
    while True:
        try:
            _ensure_table()
            _scan_recordings(settings)
            _snapshot_db(settings)
            _backup_config(settings)
            consecutive_failures = 0
        except Exception as e:
            consecutive_failures += 1
            logger.warning(f"Backup scan failed ({consecutive_failures} in a row): {e}")

        # Back off up to 15 minutes when the backend is unreachable.
        delay = min(settings["scan_interval"] * (2 ** min(consecutive_failures, 4)), 900)
        time.sleep(delay)


def start_backup_thread():
    """Start the backup daemon thread if the feature is enabled. Never raises."""
    global _backup_thread
    try:
        settings = get_backup_settings()
        if not settings:
            logger.info("S3 backup disabled")
            return

        with _thread_lock:
            if _backup_thread is None or not _backup_thread.is_alive():
                _backup_thread = threading.Thread(
                    target=_run_loop,
                    args=(settings,),
                    name="BackupThread",
                    daemon=True,
                )
                _backup_thread.start()
                logger.info(
                    f"S3 backup started (scan every {settings['scan_interval']}s, "
                    f"DB snapshot every {settings['db_snapshot_interval'] // 3600}h)"
                )
    except Exception as e:
        logger.error(f"Failed to start backup service: {e}", exc_info=True)
