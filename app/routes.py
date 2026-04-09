"""Flask routes and blueprints."""
import os
import json
import calendar
from collections import OrderedDict
from datetime import date, timedelta
from functools import wraps
from flask import Blueprint, render_template, request, abort, send_from_directory, redirect, url_for, make_response, jsonify
from werkzeug.utils import safe_join
from user_agents import parse

import app.models as models
from app.config import BRANDING, PRANK_PASSWORD, ADMIN_PASSWORD
from app.services.radio_manager import get_radio_status
from app.services.file_processor import inject_fake_message
from app.utils import (
    require_password,
    verify_password,
    log_login_attempt,
    create_authenticated_response,
    parse_time_from_filename,
    extract_radio_uid_from_filename,
    format_date_display,
    format_date_database,
    get_wav_length,
    get_unit_info,
    _load_unit_config,
)

_PRANK_COOKIE = "prank_auth"

# Simple time-based response cache
_response_cache = {}

def timed_cache(seconds):
    """Decorator that caches a view function's response for a given TTL."""
    def decorator(f):
        @wraps(f)
        def wrapper(*args, **kwargs):
            import time
            key = f.__name__
            now = time.time()
            cached = _response_cache.get(key)
            if cached and now - cached[1] < seconds:
                return cached[0]
            result = f(*args, **kwargs)
            _response_cache[key] = (result, now)
            return result
        return wrapper
    return decorator

# Create blueprints
files_bp = Blueprint('files', __name__)
auth_bp = Blueprint('auth', __name__)


def setup_routes(app, record_folder: str):
    """Register blueprints and set up routes."""
    app.config['RECORD_FOLDER'] = record_folder

    @auth_bp.route("/login", methods=["GET", "POST"])
    def login():
        """Handle user login."""
        error = None
        if request.method == "POST":
            password = request.form.get("password", "")
            if verify_password(password):
                log_login_attempt(True)
                next_url = request.args.get("next") or url_for("files.index")
                return create_authenticated_response(next_url)
            else:
                log_login_attempt(False, password)
                error = "Incorrect password"

        return render_template("login.html", error=error, branding=BRANDING)

    @files_bp.route("/")
    @require_password
    @timed_cache(15)
    def index():
        """List all recording dates."""
        record_folder = app.config['RECORD_FOLDER']

        date_dirs = [
            d for d in os.listdir(record_folder)
            if os.path.isdir(os.path.join(record_folder, d))
        ]
        date_dirs.sort(reverse=True)

        # Build a set for O(1) lookup and group by month for calendar display
        date_set = set(date_dirs)
        months = OrderedDict()
        for d in date_dirs:
            try:
                year, month = int(d[:4]), int(d[4:6])
            except (ValueError, IndexError):
                continue
            key = (year, month)
            if key not in months:
                months[key] = {
                    'label': f"{calendar.month_name[month]} {year}",
                    'weeks': calendar.monthcalendar(year, month),
                    'year': year,
                    'month': month,
                }

        recent_days = []
        for i in range(10):
            d = date.today() - timedelta(days=i)
            key = d.strftime("%Y%m%d")
            recent_days.append({
                'key': key,
                'label': format_date_display(key),
            })

        radio_status = get_radio_status()
        return render_template("index.html", months=months, date_set=date_set, recent_days=recent_days, branding=BRANDING, radio_status=radio_status)

    @files_bp.route("/files/<date>")
    @require_password
    def list_files(date):
        """List all WAV files for a specific date."""
        record_folder = app.config['RECORD_FOLDER']

        user_agent_str = request.headers.get('User-Agent', '')
        ua = parse(user_agent_str)
        is_mobile = ua.is_mobile

        folder_path = os.path.join(record_folder, date)
        formatted_date = format_date_display(date)

        transcripts = models.list_transcripts(date)
        transcript_map = {
            os.path.basename(t['filename']): t
            for t in transcripts
        }

        files = []
        for filename in sorted(os.listdir(folder_path), reverse=True):
            if not filename.endswith(".wav"):
                continue

            # Use cached duration from DB, fall back to reading the file
            db_record = transcript_map.get(filename)
            if db_record and db_record.get('duration'):
                file_length = db_record['duration']
            else:
                file_path = os.path.join(folder_path, filename)
                file_length = get_wav_length(file_path)

            if file_length < 0.5:
                continue

            formatted_time = parse_time_from_filename(filename)
            transcript = db_record['transcript'] if db_record else None
            radio_uid = extract_radio_uid_from_filename(filename)
            unit_name = get_unit_info(radio_uid) if radio_uid else None

            files.append((
                filename,
                formatted_time,
                timedelta(seconds=round(file_length, 2)),
                transcript,
                unit_name
            ))

        return render_template(
            "files.html",
            files=files,
            date=date,
            formatted_date=formatted_date,
            is_mobile=is_mobile,
            branding=BRANDING
        )

    @files_bp.route("/play/<date>/<filename>")
    @require_password
    def play_file(date, filename):
        """Serve an audio file."""
        record_folder = app.config['RECORD_FOLDER']

        if "/" in filename or "\\" in filename:
            abort(400, "Invalid filename")

        folder_path = os.path.join(record_folder, date)
        safe_path = safe_join(folder_path, filename)

        if not safe_path:
            abort(400, "Invalid path")

        if not os.path.isfile(safe_path):
            abort(404, "File not found")

        response = make_response(send_from_directory(folder_path, filename))
        response.headers['Cache-Control'] = 'public, max-age=31536000, immutable'
        return response

    @files_bp.route("/status")
    @require_password
    @timed_cache(15)
    def status():
        """Show system status: restarts, storage, recording count."""
        record_folder = app.config['RECORD_FOLDER']

        # Storage and recording count
        total_bytes = 0
        total_recordings = 0
        if os.path.isdir(record_folder):
            for root, _dirs, files in os.walk(record_folder):
                for fname in files:
                    if fname.endswith(".wav"):
                        total_recordings += 1
                        try:
                            total_bytes += os.path.getsize(os.path.join(root, fname))
                        except OSError:
                            pass

        # Human-readable storage
        for unit in ["B", "KB", "MB", "GB", "TB"]:
            if total_bytes < 1024:
                storage_used = f"{total_bytes:.1f} {unit}"
                break
            total_bytes /= 1024
        else:
            storage_used = f"{total_bytes:.1f} TB"

        restarts = models.get_restarts()
        radio_status = get_radio_status()
        return render_template(
            "status.html",
            branding=BRANDING,
            radio_status=radio_status,
            restarts=restarts,
            storage_used=storage_used,
            total_recordings=total_recordings,
        )

    @files_bp.route("/search")
    @files_bp.route("/search/<query>")
    @require_password
    def search(query=None):
        """Search transcripts."""
        query = query or request.args.get("query", "")
        results = models.search_transcripts_by_string(query) if query else []

        output = []
        for result in results:
            full_path = result["filename"]
            filename = os.path.basename(full_path)

            path_parts = full_path.replace("\\", "/").split("/")
            date = path_parts[-2] if len(path_parts) >= 2 else None

            formatted_time = parse_time_from_filename(filename)
            radio_uid = extract_radio_uid_from_filename(filename)
            unit_name = get_unit_info(radio_uid) if radio_uid else None

            output.append({
                "timestamp": result["timestamp"],
                "formatted_time": formatted_time,
                "transcript": result["transcript"],
                "filename": filename,
                "date": date,
                "unit_name": unit_name
            })

        output.reverse()
        return render_template("search_results.html", query=query, results=output, branding=BRANDING)

    @files_bp.route("/prank", methods=["GET", "POST"])
    def prank():
        """Hidden prank message injector. Password-protected separately from the main site."""
        error = None
        success = None
        is_authed = request.cookies.get(_PRANK_COOKIE) == PRANK_PASSWORD

        if request.method == "POST":
            action = request.form.get("action", "")

            if action == "auth":
                password = request.form.get("password", "")
                if password == PRANK_PASSWORD:
                    resp = make_response(redirect(url_for("files.prank")))
                    resp.set_cookie(_PRANK_COOKIE, PRANK_PASSWORD, max_age=60 * 60 * 2, httponly=True, samesite="Lax")
                    return resp
                error = "Wrong password"

            elif action == "cleanup" and is_authed:
                filenames = models.delete_prank_transcripts()
                deleted_files = 0
                for fname in filenames:
                    try:
                        if os.path.isfile(fname):
                            os.remove(fname)
                            deleted_files += 1
                    except OSError:
                        pass
                success = {'cleanup': True, 'count': len(filenames), 'files_deleted': deleted_files}

            elif action == "inject" and is_authed:
                transcript = request.form.get("transcript", "").strip()
                unit_id_raw = request.form.get("unit_id", "").strip()
                custom_unit_name = request.form.get("custom_unit_name", "").strip()

                if not transcript:
                    error = "Transcript text is required"
                else:
                    try:
                        unit_id = int(unit_id_raw) if unit_id_raw else 9999
                    except ValueError:
                        unit_id = 9999

                    unit_map = _load_unit_config()
                    if custom_unit_name:
                        unit_name = custom_unit_name
                    else:
                        unit_name = unit_map.get(unit_id, f"Unknown. Radio ID: {unit_id}")

                    record_folder = app.config['RECORD_FOLDER']
                    result = inject_fake_message(transcript, unit_id, unit_name, record_folder)
                    if result:
                        success = result
                    else:
                        error = "Failed to create fake message"

        unit_map = _load_unit_config() if is_authed else {}
        prank_count = len(models.get_prank_transcripts()) if is_authed else 0
        return render_template(
            "prank.html",
            branding=BRANDING,
            is_authed=is_authed,
            error=error,
            success=success,
            units=unit_map,
            prank_count=prank_count,
        )

    _ADMIN_COOKIE = "admin_auth"

    @files_bp.route("/admin", methods=["GET", "POST"])
    def admin():
        """Admin page for comparing Deepgram vs Moonshine transcriptions side by side."""
        error = None
        success = None
        comparison = None
        selected_date = None
        is_authed = request.cookies.get(_ADMIN_COOKIE) == ADMIN_PASSWORD

        record_folder = app.config['RECORD_FOLDER']

        if request.method == "POST":
            action = request.form.get("action", "")

            if action == "auth":
                password = request.form.get("password", "")
                if password == ADMIN_PASSWORD:
                    resp = make_response(redirect(url_for("files.admin")))
                    resp.set_cookie(_ADMIN_COOKIE, ADMIN_PASSWORD, max_age=60 * 60 * 24, httponly=True, samesite="Lax")
                    return resp
                error = "Wrong password"

        # Build comparison data if we have a date
        if is_authed:
            # Get available dates
            date_dirs = []
            if os.path.isdir(record_folder):
                date_dirs = sorted(
                    [d for d in os.listdir(record_folder) if os.path.isdir(os.path.join(record_folder, d))],
                    reverse=True
                )

            # If date selected (via GET param or POST), load comparison
            selected_date = selected_date or request.args.get("date", "")
            if selected_date:
                deepgram_transcripts = models.list_transcripts(selected_date)
                moonshine_transcripts = models.list_moonshine_transcripts(selected_date)

                dg_map = {os.path.basename(t['filename']): t['transcript'] for t in deepgram_transcripts}
                ms_map = {os.path.basename(t['filename']): t['transcript'] for t in moonshine_transcripts}

                all_files = sorted(set(list(dg_map.keys()) + list(ms_map.keys())))

                comparison = []
                for filename in all_files:
                    formatted_time = parse_time_from_filename(filename)
                    radio_uid = extract_radio_uid_from_filename(filename)
                    unit_name = get_unit_info(radio_uid) if radio_uid else None
                    comparison.append({
                        'filename': filename,
                        'time': formatted_time,
                        'unit_name': unit_name,
                        'deepgram': dg_map.get(filename, ''),
                        'moonshine': ms_map.get(filename, ''),
                    })
        else:
            date_dirs = []

        return render_template(
            "admin.html",
            branding=BRANDING,
            is_authed=is_authed,
            error=error,
            success=success,
            date_dirs=date_dirs,
            selected_date=selected_date or "",
            comparison=comparison,
        )

    @files_bp.route("/admin/files/<date>")
    def admin_list_files(date):
        """Return JSON list of WAV files for a date that need Moonshine transcription."""
        if request.cookies.get(_ADMIN_COOKIE) != ADMIN_PASSWORD:
            return jsonify({"error": "unauthorized"}), 401

        record_folder = app.config['RECORD_FOLDER']
        folder_path = os.path.join(record_folder, date)
        if not os.path.isdir(folder_path):
            return jsonify({"error": "no recordings for this date"}), 404

        files = []
        for filename in sorted(os.listdir(folder_path)):
            if not filename.endswith(".wav"):
                continue
            file_path = os.path.join(folder_path, filename)
            file_length = get_wav_length(file_path)
            if file_length < 0.5:
                continue
            files.append(filename)

        return jsonify({"files": files, "total": len(files)})

    @files_bp.route("/admin/transcribe", methods=["POST"])
    def admin_transcribe_one():
        """Transcribe a single file with Moonshine and return the result."""
        if request.cookies.get(_ADMIN_COOKIE) != ADMIN_PASSWORD:
            return jsonify({"error": "unauthorized"}), 401

        data = request.get_json()
        date_str = data.get("date", "")
        filename = data.get("filename", "")

        if not date_str or not filename:
            return jsonify({"error": "date and filename required"}), 400
        if "/" in filename or "\\" in filename:
            return jsonify({"error": "invalid filename"}), 400

        record_folder = app.config['RECORD_FOLDER']
        file_path = os.path.join(record_folder, date_str, filename)

        if not os.path.isfile(file_path):
            return jsonify({"error": "file not found"}), 404

        from app.services.moonshine_transcription import get_moonshine_transcription
        moonshine_text = get_moonshine_transcription(file_path)
        models.save_moonshine_transcript(file_path, moonshine_text)

        return jsonify({"filename": filename, "transcript": moonshine_text})

    # Register blueprints
    app.register_blueprint(files_bp, url_prefix='')
    app.register_blueprint(auth_bp, url_prefix='')