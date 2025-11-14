"""Utility functions for the application."""
import os
import logging
import json
from datetime import datetime
from functools import wraps
from flask import request, redirect, url_for, make_response
import wave
import yaml

from app.config import APP_PASSWORD


_login_logger = None


def get_login_logger():
    """Get or create the login logger."""
    global _login_logger
    if _login_logger is None:
        _login_logger = logging.getLogger("login_logger")
        if not _login_logger.handlers:
            handler = logging.FileHandler("login.log")
            formatter = logging.Formatter('%(asctime)s %(message)s')
            handler.setFormatter(formatter)
            _login_logger.addHandler(handler)
            _login_logger.setLevel(logging.INFO)
    return _login_logger


def check_auth() -> bool:
    """Check if user has valid password cookie."""
    cookie_pw = request.cookies.get("site_pw")
    return cookie_pw == APP_PASSWORD


def verify_password(password: str) -> bool:
    """Verify password is correct."""
    return password == APP_PASSWORD


def require_password(view_func):
    """Decorator to require password authentication."""
    @wraps(view_func)
    def wrapped_view(*args, **kwargs):
        if check_auth():
            return view_func(*args, **kwargs)
        return redirect(url_for('auth.login', next=request.path))
    return wrapped_view


def log_login_attempt(success: bool, password: str = ""):
    """Log a login attempt."""
    logger = get_login_logger()
    ip = request.headers.get('X-Forwarded-For', request.remote_addr)
    user_agent = request.headers.get('User-Agent', '')

    message = f"Login attempt: success={success} ip={ip} user_agent={json.dumps(user_agent)}"
    if not success and password:
        message += f" attempted_password={json.dumps(password)}"

    logger.info(message)


def create_authenticated_response(next_url: str = None):
    """Create a response with authentication cookie."""
    next_url = next_url or url_for("files.index")
    resp = make_response(redirect(next_url))
    resp.set_cookie("site_pw", APP_PASSWORD, max_age=60*60*24*30, httponly=True, samesite="Lax")
    return resp


# File utilities
def parse_time_from_filename(filename: str) -> str:
    """Parse time from filename and return formatted time string."""
    try:
        # Filename format: YYYYMMDD_HHMMSS_...
        # Split and get the second part (index 1) which is the time
        parts = filename.split("_")
        if len(parts) < 2:
            return filename

        time_str = parts[1]
        if len(time_str) == 6:
            hours = int(time_str[:2])
            minutes = int(time_str[2:4])
            seconds = int(time_str[4:6])
        elif len(time_str) == 5:
            hours = int(time_str[0])
            minutes = int(time_str[1:3])
            seconds = int(time_str[3:5])
        else:
            return filename

        recording_time = datetime.strptime(
            f"{hours:02}:{minutes:02}:{seconds:02}", "%H:%M:%S"
        )
        return recording_time.strftime("%I:%M:%S %p")
    except (ValueError, TypeError, IndexError):
        return filename


def extract_radio_uid_from_filename(filename: str) -> int | None:
    """Extract radio unit ID from filename."""
    try:
        basename = os.path.splitext(filename)[0]
        radio_uid = basename.split("_")[-1]
        return int(radio_uid) if radio_uid.isdigit() else None
    except (ValueError, IndexError):
        return None


def format_date_display(date_str: str) -> str:
    """Format date from YYYYMMDD to YYYY/MM/DD."""
    if len(date_str) == 8:
        return f"{date_str[:4]}/{date_str[4:6]}/{date_str[6:]}"
    return date_str


def format_date_database(date_str: str) -> str:
    """Format date from YYYYMMDD to YYYY-MM-DD."""
    if len(date_str) == 8:
        return f"{date_str[:4]}-{date_str[4:6]}-{date_str[6:]}"
    return date_str


def get_wav_length(file_path: str) -> float:
    """Get the duration of a WAV file in seconds."""
    try:
        with wave.open(file_path, 'rb') as wav_file:
            frames = wav_file.getnframes()
            rate = wav_file.getframerate()
            duration = frames / float(rate)
            return duration
    except Exception:
        return 0.0


# ID Mapping
_unit_config_cache = None


def _load_unit_config() -> dict:
    """Load unit configuration from YAML file with caching."""
    global _unit_config_cache
    if _unit_config_cache is None:
        config_path = os.path.join(os.path.dirname(os.path.dirname(__file__)), "config.yaml")
        try:
            with open(config_path, 'r') as f:
                # Load config and convert keys to integers
                config = yaml.safe_load(f)
                units = config.get("units", {})
                _unit_config_cache = {int(k): v for k, v in units.items()}
        except (FileNotFoundError, yaml.YAMLError) as e:
            logging.warning(f"Failed to load config.yaml for units: {e}")
            _unit_config_cache = {}
    return _unit_config_cache


def get_unit_info(unit_id: int) -> str:
    """Map radio unit ID to unit name from config file."""
    unit_map = _load_unit_config()
    return unit_map.get(unit_id, f"Unknown. Radio ID: {unit_id}")
