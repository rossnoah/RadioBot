"""Radio process manager for controlling the RTL-SDR receiver."""
import os
import subprocess
import logging
import signal
import time
from typing import Optional
from app.config import get_config

logger = logging.getLogger(__name__)


class RadioManager:
    """Manages the dsd-fme radio monitoring process."""

    def __init__(self):
        """Initialize the radio manager."""
        self.process: Optional[subprocess.Popen] = None
        self.config = get_config().get("radio", {})
        self._validate_config()

    def _validate_config(self):
        """Validate required radio configuration."""
        required_fields = ["frequency", "gain"]
        missing_fields = [field for field in required_fields if field not in self.config]

        if missing_fields:
            raise ValueError(
                f"Missing required radio configuration fields: {', '.join(missing_fields)}. "
                "Please update config.yaml with radio settings."
            )

    def _build_command(self) -> list:
        """Build the dsd-fme command from configuration.

        Returns:
            List of command arguments for subprocess
        """
        # Get configuration values (only device, frequency, and gain are configurable)
        frequency = self.config["frequency"]
        gain = self.config["gain"]
        device_index = self.config.get("device_index", 0)

        # Hard-coded values for RTL-SDR input
        # Format: rtl:dev:freq:gain:ppm:bw:sq:vol
        # ppm=0, bandwidth=12, squelch=0, volume=2
        rtl_input = f"rtl:{device_index}:{frequency}M:{gain}:0:12:0:3"

        # Ensure directories exist
        temp_dir = "./temp"
        for directory in [temp_dir]:
            if not os.path.exists(directory):
                os.makedirs(directory)
                logger.info(f"Created directory: {directory}")

        # Build command based on: dsd-fme -fs -i rtl:0:461.375M:32:0:12:0:2 -P -7 calls -Q dmr_log.jsonl -J events.txt -a -t 1 -o null

        command = [
            "dsd-fme",
            "-fs",  # DMR Stereo mode
            "-i", rtl_input,  # RTL-SDR input specification
            "-P", "-7", temp_dir,  # Per-call wav files output directory
            "-Q", "dmr_log.jsonl",  # DMR log file
            "-J", "events.txt",  # Events file
            "-a",  # Auto-detect frame type
            "-t", "1",  # Frame timeout
            "-o", "null"  # No audio output (null)
        ]

        return command

    def start(self):
        """Start the radio monitoring process."""
        if self.is_running():
            logger.warning("Radio process is already running")
            return

        try:
            command = self._build_command()
            logger.info(f"Starting radio process: {' '.join(command)}")

            # Open log file for dsd-fme stderr output (contains main output)
            log_file = open("dsd-fme.jsonl", "a")

            # Start the process
            # Note: dsd-fme outputs to stderr, not stdout
            self.process = subprocess.Popen(
                command,
                stdout=subprocess.PIPE,
                stderr=log_file,
                text=True
            )

            # Store log file handle for cleanup
            self._log_file = log_file

            # Give it a moment to start
            time.sleep(1)

            # Check if it started successfully
            if self.process.poll() is not None:
                # Process already exited
                logger.error(f"Radio process failed to start. Exit code: {self.process.returncode}")
                logger.error(f"Check dsd-fme.jsonl for details")
                log_file.close()
                self.process = None
                raise RuntimeError("Failed to start radio process. Check logs for details.")

            logger.info(f"Radio process started successfully (PID: {self.process.pid})")
            logger.info(f"Monitoring DMR on {self.config['frequency']} MHz (gain: {self.config['gain']})")
            logger.info(f"Logs: dsd-fme.jsonl | Call recordings: temp/")

        except FileNotFoundError:
            logger.error("dsd-fme command not found. Please ensure it is installed and in PATH.")
            raise
        except Exception as e:
            logger.error(f"Error starting radio process: {e}")
            self.process = None
            raise

    def stop(self):
        """Stop the radio monitoring process."""
        if not self.is_running():
            logger.warning("Radio process is not running")
            return

        try:
            logger.info(f"Stopping radio process (PID: {self.process.pid})")

            # Try graceful shutdown first
            self.process.terminate()

            # Wait up to 5 seconds for graceful shutdown
            try:
                self.process.wait(timeout=5)
                logger.info("Radio process stopped gracefully")
            except subprocess.TimeoutExpired:
                # Force kill if it doesn't stop gracefully
                logger.warning("Radio process did not stop gracefully, force killing")
                self.process.kill()
                self.process.wait()
                logger.info("Radio process force killed")

            # Close log file if it exists
            if hasattr(self, '_log_file') and self._log_file:
                self._log_file.close()
                self._log_file = None

            self.process = None

        except Exception as e:
            logger.error(f"Error stopping radio process: {e}")
            raise

    def restart(self):
        """Restart the radio monitoring process."""
        logger.info("Restarting radio process")
        self.stop()
        time.sleep(1)
        self.start()

    def is_running(self) -> bool:
        """Check if the radio process is running.

        Returns:
            True if process is running, False otherwise
        """
        if self.process is None:
            return False

        # Check if process is still alive
        return self.process.poll() is None

    def get_status(self) -> dict:
        """Get the current status of the radio process.

        Returns:
            Dictionary with status information
        """
        is_running = self.is_running()
        status = {
            "running": is_running,
            "pid": self.process.pid if is_running else None,
            "config": {
                "frequency": self.config["frequency"],
                "gain": self.config["gain"],
                "device_index": self.config.get("device_index", 0),
            }
        }
        return status


# Global radio manager instance
_radio_manager: Optional[RadioManager] = None


def get_radio_manager() -> RadioManager:
    """Get or create the global radio manager instance."""
    global _radio_manager
    if _radio_manager is None:
        _radio_manager = RadioManager()
    return _radio_manager


def start_radio():
    """Start the radio monitoring process."""
    manager = get_radio_manager()
    manager.start()


def stop_radio():
    """Stop the radio monitoring process."""
    manager = get_radio_manager()
    manager.stop()


def restart_radio():
    """Restart the radio monitoring process."""
    manager = get_radio_manager()
    manager.restart()


def is_radio_running() -> bool:
    """Check if the radio process is running."""
    manager = get_radio_manager()
    return manager.is_running()


def get_radio_status() -> dict:
    """Get the current radio status."""
    manager = get_radio_manager()
    return manager.get_status()
