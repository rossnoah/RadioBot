"""Main Flask application for radio recording server."""
import os
import sys
import logging
import signal
import atexit
from flask import Flask
from flask_socketio import SocketIO

import app.models as models
import app.services.file_organizer as file_organizer
import app.services.radio_manager as radio_manager
from app.routes import setup_routes
from app.config import APP_PASSWORD


# Create folders
os.makedirs("logs", exist_ok=True)
os.makedirs("temp", exist_ok=True)
os.makedirs("files", exist_ok=True)


# Configure logging
logging.basicConfig(
    level=logging.INFO,
    format='%(asctime)s - %(name)s - %(levelname)s - %(message)s'
)
logger = logging.getLogger(__name__)

# Create Flask app
app = Flask(__name__)
socketio = SocketIO(app)

# Set up socketio for file organizer
file_organizer.set_socketio(socketio)

# Register routes and blueprints (use "files" as the record folder)
setup_routes(app, "files")

# Error handlers
@app.errorhandler(404)
def page_not_found(e):
    """Handle 404 errors."""
    from flask import render_template
    from app.config import BRANDING
    return render_template('404.html', branding=BRANDING), 404


def cleanup_handler():
    """Cleanup handler to stop radio process on exit."""
    logger.info("Shutting down... stopping radio process")
    try:
        radio_manager.stop_radio()
    except Exception as e:
        logger.error(f"Error stopping radio process during cleanup: {e}")


def signal_handler(signum, frame):
    """Handle interrupt signals gracefully."""
    logger.info(f"Received signal {signum}")
    cleanup_handler()
    sys.exit(0)


# Initialize database and start background threads
if __name__ == "__main__":
    # Register cleanup handlers
    atexit.register(cleanup_handler)
    signal.signal(signal.SIGINT, signal_handler)
    signal.signal(signal.SIGTERM, signal_handler)

    # Initialize database
    models.init_db()
    logger.info("Database initialized")

    # Start file organizer
    file_organizer.start_organizer_thread()
    logger.info("File organizer started")

    # Start radio monitoring process
    try:
        radio_manager.start_radio()
        status = radio_manager.get_radio_status()
        logger.info(f"Radio monitoring started: {status['config']['frequency']} MHz, gain: {status['config']['gain']} dB")
    except Exception as e:
        logger.error(f"Failed to start radio process: {e}")
        logger.error("Server will continue without radio monitoring. Please check your configuration and dsd-fme installation.")

    # Start Flask server
    logger.info("Starting Flask-SocketIO server on port 4000...")
    socketio.run(app, debug=False, port=4000)
