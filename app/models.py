"""Database models and operations."""
import sqlite3
import os
import logging
from datetime import datetime

logger = logging.getLogger(__name__)

DATABASE_FILE = "transcripts.db"


def get_db_connection():
    """Create a database connection with WAL mode enabled."""
    conn = sqlite3.connect(DATABASE_FILE, timeout=10)
    conn.row_factory = sqlite3.Row
    conn.execute("PRAGMA journal_mode=WAL")
    return conn


def init_db():
    """Initialize the database with required tables."""
    conn = get_db_connection()
    try:
        cursor = conn.cursor()

        cursor.execute('''
            CREATE TABLE IF NOT EXISTS transcripts (
                id INTEGER PRIMARY KEY AUTOINCREMENT,
                filename TEXT UNIQUE NOT NULL,
                transcript TEXT NOT NULL,
                response TEXT NOT NULL,
                timestamp TEXT NOT NULL
            )
        ''')

        cursor.execute('''
            CREATE INDEX IF NOT EXISTS idx_filename ON transcripts (filename)
        ''')

        # Migration: add is_fake column if it doesn't exist yet
        try:
            cursor.execute('ALTER TABLE transcripts ADD COLUMN is_fake INTEGER NOT NULL DEFAULT 0')
        except Exception:
            pass  # Column already exists

        cursor.execute('''
            CREATE TABLE IF NOT EXISTS restarts (
                id INTEGER PRIMARY KEY AUTOINCREMENT,
                timestamp TEXT NOT NULL,
                reason TEXT NOT NULL,
                uptime_seconds INTEGER
            )
        ''')

        conn.commit()
    finally:
        conn.close()


def log_restart(reason: str, uptime_seconds=None):
    """Log a radio restart event."""
    conn = get_db_connection()
    try:
        timestamp = datetime.now().strftime("%Y-%m-%d %H:%M:%S")
        conn.execute(
            'INSERT INTO restarts (timestamp, reason, uptime_seconds) VALUES (?, ?, ?)',
            (timestamp, reason, uptime_seconds)
        )
        conn.commit()
    finally:
        conn.close()


def get_restarts(limit=100):
    """Get recent restart events, newest first."""
    conn = get_db_connection()
    try:
        cursor = conn.execute(
            'SELECT * FROM restarts ORDER BY id DESC LIMIT ?',
            (limit,)
        )
        return [dict(row) for row in cursor.fetchall()]
    finally:
        conn.close()


def save_transcript(filename, transcript, api_response, is_fake: bool = False):
    """Save or update a transcript in the database."""
    conn = get_db_connection()
    try:
        timestamp = datetime.now().strftime("%Y-%m-%d %H:%M:%S")
        conn.execute('''
            INSERT OR REPLACE INTO transcripts (filename, transcript, response, timestamp, is_fake)
            VALUES (?, ?, ?, ?, ?)
        ''', (filename, transcript, api_response, timestamp, 1 if is_fake else 0))
        conn.commit()
    finally:
        conn.close()


def get_prank_transcripts():
    """Return all prank/fake transcripts."""
    conn = get_db_connection()
    try:
        cursor = conn.execute('SELECT * FROM transcripts WHERE is_fake = 1 ORDER BY timestamp DESC')
        return [dict(row) for row in cursor.fetchall()]
    finally:
        conn.close()


def delete_prank_transcripts():
    """Delete all prank/fake transcripts and return their filenames."""
    conn = get_db_connection()
    try:
        cursor = conn.execute('SELECT filename FROM transcripts WHERE is_fake = 1')
        filenames = [row['filename'] for row in cursor.fetchall()]
        conn.execute('DELETE FROM transcripts WHERE is_fake = 1')
        conn.commit()
        return filenames
    finally:
        conn.close()


def get_transcript(filename):
    """Get a transcript by filename."""
    conn = get_db_connection()
    try:
        cursor = conn.execute('SELECT transcript FROM transcripts WHERE filename = ?', (filename,))
        result = cursor.fetchone()
        return result['transcript'] if result else None
    finally:
        conn.close()


def list_transcripts(date=None):
    """List all transcripts, optionally filtered by date."""
    conn = get_db_connection()
    try:
        if date:
            cursor = conn.execute(
                'SELECT * FROM transcripts WHERE filename LIKE ? ORDER BY timestamp DESC',
                (f'%{date}%',)
            )
        else:
            cursor = conn.execute('SELECT * FROM transcripts ORDER BY timestamp DESC')
        return [dict(row) for row in cursor.fetchall()]
    finally:
        conn.close()


def list_transcripts_filenames(date=None):
    """List all transcript filenames, optionally filtered by date."""
    conn = get_db_connection()
    try:
        if date:
            cursor = conn.execute(
                'SELECT filename FROM transcripts WHERE filename LIKE ? ORDER BY timestamp DESC',
                (f'%{date}%',)
            )
        else:
            cursor = conn.execute('SELECT filename FROM transcripts ORDER BY timestamp DESC')
        return [row['filename'] for row in cursor.fetchall()]
    finally:
        conn.close()


def search_transcripts_by_string(search_string):
    """Search transcripts by keyword."""
    conn = get_db_connection()
    try:
        cursor = conn.execute(
            'SELECT * FROM transcripts WHERE transcript LIKE ? ORDER BY timestamp DESC',
            (f'%{search_string}%',)
        )
        return [dict(row) for row in cursor.fetchall()]
    finally:
        conn.close()
