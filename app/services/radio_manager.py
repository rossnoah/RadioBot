"""Radio process manager for controlling the RTL-SDR receiver."""
import os
import subprocess
import logging
import signal
import time
import threading
from typing import Optional
from app.config import get_config

logger = logging.getLogger(__name__)

# Watchdog settings
RESTART_INTERVAL = 3600  # Restart the process every hour (seconds)
FROZEN_CHECK_INTERVAL = 30  # How often to check for frozen process (seconds)
FROZEN_TIMEOUT = 300  # Consider process frozen if no log output for 5 minutes (seconds)


class RadioManager:
    """Manages the dsd-fme radio monitoring process."""

    def __init__(self):
        """Initialize the radio manager."""
        self.process: Optional[subprocess.Popen] = None
        self.config = get_config().get("radio", {})
        self._validate_config()
        self._last_start_time: Optional[float] = None
        self._watchdog_thread: Optional[threading.Thread] = None
        self._watchdog_running = False

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

            self._last_start_time = time.time()

            logger.info(f"Radio process started successfully (PID: {self.process.pid})")
            logger.info(f"Monitoring DMR on {self.config['frequency']} MHz (gain: {self.config['gain']})")
            logger.info(f"Logs: dsd-fme.jsonl | Call recordings: temp/")

            # Start watchdog if not already running
            self._start_watchdog()

        except FileNotFoundError:
            logger.error("dsd-fme command not found. Please ensure it is installed and in PATH.")
            raise
        except Exception as e:
            logger.error(f"Error starting radio process: {e}")
            self.process = None
            raise

    def stop(self, stop_watchdog=True):
        """Stop the radio monitoring process.

        Args:
            stop_watchdog: If True, also stop the watchdog thread. Set to False
                          when the watchdog itself is triggering a restart.
        """
        if stop_watchdog:
            self._stop_watchdog()

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
            self._last_start_time = None

        except Exception as e:
            logger.error(f"Error stopping radio process: {e}")
            raise

    def restart(self):
        """Restart the radio monitoring process."""
        logger.info("Restarting radio process")
        self.stop()
        time.sleep(1)
        self.start()

    def _start_watchdog(self):
        """Start the watchdog thread that monitors process health."""
        if self._watchdog_running:
            return

        self._watchdog_running = True
        self._watchdog_thread = threading.Thread(
            target=self._watchdog_loop,
            name="RadioWatchdogThread",
            daemon=True
        )
        self._watchdog_thread.start()
        logger.info("Radio watchdog started")

    def _stop_watchdog(self):
        """Stop the watchdog thread."""
        if not self._watchdog_running:
            return

        self._watchdog_running = False
        if self._watchdog_thread and self._watchdog_thread.is_alive():
            self._watchdog_thread.join(timeout=10)
        self._watchdog_thread = None
        logger.info("Radio watchdog stopped")

    def _is_process_frozen(self) -> bool:
        """Check if the radio process appears frozen by monitoring log file activity.

        Returns:
            True if the process appears frozen, False otherwise.
        """
        log_file = "dsd-fme.jsonl"
        try:
            if os.path.exists(log_file):
                last_modified = os.path.getmtime(log_file)
                seconds_since_update = time.time() - last_modified
                if seconds_since_update > FROZEN_TIMEOUT:
                    logger.warning(
                        f"Log file hasn't been updated in {seconds_since_update:.0f}s "
                        f"(threshold: {FROZEN_TIMEOUT}s)"
                    )
                    return True
        except OSError as e:
            logger.error(f"Error checking log file: {e}")

        return False

    def _watchdog_loop(self):
        """Background loop that monitors and restarts the radio process."""
        logger.info(
            f"Watchdog active: periodic restart every {RESTART_INTERVAL}s, "
            f"frozen detection after {FROZEN_TIMEOUT}s of inactivity"
        )

        while self._watchdog_running:
            try:
                # Sleep in short intervals so we can stop quickly
                for _ in range(FROZEN_CHECK_INTERVAL):
                    if not self._watchdog_running:
                        return
                    time.sleep(1)

                if not self._watchdog_running:
                    return

                # Check if process died unexpectedly
                if not self.is_running() and self._last_start_time is not None:
                    logger.warning("Radio process died unexpectedly, restarting...")
                    try:
                        # Clean up the dead process
                        if hasattr(self, '_log_file') and self._log_file:
                            self._log_file.close()
                            self._log_file = None
                        self.process = None
                        self._last_start_time = None
                        time.sleep(2)
                        self.start()
                    except Exception as e:
                        logger.error(f"Watchdog failed to restart after crash: {e}")
                    continue

                if not self.is_running():
                    continue

                # Check for periodic restart (every hour)
                if self._last_start_time is not None:
                    uptime = time.time() - self._last_start_time
                    if uptime >= RESTART_INTERVAL:
                        logger.info(
                            f"Periodic restart triggered (uptime: {uptime:.0f}s / "
                            f"{uptime / 3600:.1f}h)"
                        )
                        self._do_watchdog_restart("periodic restart")
                        continue

                # Check for frozen process
                if self._is_process_frozen():
                    logger.warning("Process appears frozen, restarting...")
                    self._do_watchdog_restart("frozen process detected")

            except Exception as e:
                logger.error(f"Watchdog error: {e}", exc_info=True)
                # Don't let the watchdog die from an unexpected error
                time.sleep(10)

    def _do_watchdog_restart(self, reason: str):
        """Perform a restart triggered by the watchdog.

        Args:
            reason: Human-readable reason for the restart.
        """
        try:
            logger.info(f"Watchdog restart reason: {reason}")
            self.stop(stop_watchdog=False)
            time.sleep(2)
            self.start()
        except Exception as e:
            logger.error(f"Watchdog restart failed ({reason}): {e}")

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
        uptime = None
        next_restart = None
        if is_running and self._last_start_time is not None:
            uptime = int(time.time() - self._last_start_time)
            next_restart = max(0, RESTART_INTERVAL - uptime)

        status = {
            "running": is_running,
            "pid": self.process.pid if is_running else None,
            "uptime_seconds": uptime,
            "next_restart_seconds": next_restart,
            "watchdog_active": self._watchdog_running,
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
