"""Flask routes and blueprints."""
import os
from datetime import timedelta
from flask import Blueprint, render_template, request, abort, send_from_directory, redirect, url_for
from werkzeug.utils import safe_join
from user_agents import parse

import app.models as models
from app.config import BRANDING
from app.services.radio_manager import get_radio_status
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
)

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
    def index():
        """List all recording dates."""
        record_folder = app.config['RECORD_FOLDER']

        date_dirs = [
            d for d in os.listdir(record_folder)
            if os.path.isdir(os.path.join(record_folder, d))
        ]
        date_dirs.sort(reverse=True)

        date_info = [(date, format_date_display(date)) for date in date_dirs]
        radio_status = get_radio_status()
        return render_template("index.html", date_info=date_info, branding=BRANDING, radio_status=radio_status)

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
            os.path.basename(t['filename']): t['transcript']
            for t in transcripts
        }

        files = []
        for filename in sorted(os.listdir(folder_path), reverse=True):
            if not filename.endswith(".wav"):
                continue

            file_path = os.path.join(folder_path, filename)
            file_length = get_wav_length(file_path)

            if file_length < 0.5:
                continue

            formatted_time = parse_time_from_filename(filename)
            transcript = transcript_map.get(filename)
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

        return send_from_directory(folder_path, filename)

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

    # Register blueprints
    app.register_blueprint(files_bp, url_prefix='')
    app.register_blueprint(auth_bp, url_prefix='')