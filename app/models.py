"""Database models and operations."""
import sqlite3
import os
from datetime import datetime

DATABASE_FILE = "transcripts.db"


def get_db_connection():
    """Create a database connection."""
    conn = sqlite3.connect(DATABASE_FILE)
    conn.row_factory = sqlite3.Row
    return conn


def init_db():
    """Initialize the database with required tables."""
    conn = get_db_connection()
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

    conn.commit()
    conn.close()


def save_transcript(filename, transcript, api_response):
    """Save or update a transcript in the database."""
    conn = get_db_connection()
    cursor = conn.cursor()
    timestamp = datetime.now().strftime("%Y-%m-%d %H:%M:%S")

    cursor.execute('''
        INSERT OR REPLACE INTO transcripts (filename, transcript, response, timestamp)
        VALUES (?, ?, ?, ?)
    ''', (filename, transcript, api_response, timestamp))

    conn.commit()
    conn.close()


def get_transcript(filename):
    """Get a transcript by filename."""
    conn = get_db_connection()
    cursor = conn.cursor()
    cursor.execute('SELECT transcript FROM transcripts WHERE filename = ?', (filename,))
    result = cursor.fetchone()
    conn.close()
    return result['transcript'] if result else None


def list_transcripts(date=None):
    """List all transcripts, optionally filtered by date."""
    conn = get_db_connection()
    cursor = conn.cursor()

    if date:
        query = 'SELECT * FROM transcripts WHERE filename LIKE ? ORDER BY timestamp DESC'
        cursor.execute(query, (f'%{date}%',))
    else:
        cursor.execute('SELECT * FROM transcripts ORDER BY timestamp DESC')

    results = [dict(row) for row in cursor.fetchall()]
    conn.close()
    return results


def list_transcripts_filenames(date=None):
    """List all transcript filenames, optionally filtered by date."""
    conn = get_db_connection()
    cursor = conn.cursor()

    if date:
        query = 'SELECT filename FROM transcripts WHERE filename LIKE ? ORDER BY timestamp DESC'
        cursor.execute(query, (f'%{date}%',))
    else:
        cursor.execute('SELECT filename FROM transcripts ORDER BY timestamp DESC')

    results = [row['filename'] for row in cursor.fetchall()]
    conn.close()
    return results


def search_transcripts_by_string(search_string):
    """Search transcripts by keyword."""
    conn = get_db_connection()
    cursor = conn.cursor()
    query = 'SELECT * FROM transcripts WHERE transcript LIKE ? ORDER BY timestamp DESC'
    cursor.execute(query, (f'%{search_string}%',))
    results = [dict(row) for row in cursor.fetchall()]
    conn.close()
    return results
