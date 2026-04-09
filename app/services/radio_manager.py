"""Radio process manager for controlling the RTL-SDR receiver."""
import os
import subprocess
import logging
import signal
import time
import threading
from typing import Optional
from app.config import get_config
import app.models as models

PID_FILE = "dsd-fme.pid"

logger = logging.getLogger(__name__)

# Watchdog settings
FROZEN_CHECK_INTERVAL = 30  # How often to check for frozen process (seconds)
FROZEN_TIMEOUT = 1800  # Consider process frozen if no log output for 30 minutes (seconds)
MAX_CRASHES_BEFORE_REBOOT = 3  # Reboot after this many crashes in the time window
CRASH_WINDOW = 300  # Time window (seconds) for counting crashes

# RTL-SDR USB vendor ID (Realtek)
RTL_SDR_USB_VENDOR_ID = "0bda"
# This is used as part of detecting issues with the USB controller and restarting. This was implemented to restart when the USB controller dies on a Raspberry Pi 5.


class RadioManager:
    """Manages the dsd-fme radio monitoring process."""

    def __init__(self):
        """Initialize the radio manager."""
        self.process: Optional[subprocess.Popen] = None
        self.config = get_config().get("radio", {})
        self._validate_config()
        self._last_start_time: Optional[float] = None
        self._last_message_time: Optional[float] = None
        self._watchdog_thread: Optional[threading.Thread] = None
        self._watchdog_running = False
        self._crash_times: list[float] = []
        self._stopped_since: Optional[float] = None  # When process first entered stopped state

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
        # Get configuration values
        frequency = self.config["frequency"]
        gain = self.config["gain"]
        device_index = self.config.get("device_index", 0)
        ppm = self.config.get("ppm", 0)

        # Format: rtl:dev:freq:gain:ppm:bw:sq:vol
        # bandwidth=12, squelch=0, volume=3
        rtl_input = f"rtl:{device_index}:{frequency}M:{gain}:{ppm}:12:0:3"

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

    def _kill_orphaned_processes(self):
        """Find and kill any orphaned dsd-fme processes from a previous worker."""
        # Check PID file first
        if os.path.exists(PID_FILE):
            try:
                with open(PID_FILE) as f:
                    old_pid = int(f.read().strip())
                # Check if this PID is actually a dsd-fme process
                try:
                    cmdline_path = f"/proc/{old_pid}/cmdline"
                    if os.path.exists(cmdline_path):
                        with open(cmdline_path) as f:
                            cmdline = f.read()
                        if "dsd-fme" in cmdline:
                            logger.warning(f"Killing orphaned dsd-fme process (PID {old_pid}) from PID file")
                            os.kill(old_pid, signal.SIGTERM)
                            time.sleep(2)
                            try:
                                os.kill(old_pid, signal.SIGKILL)
                            except ProcessLookupError:
                                pass
                except (ProcessLookupError, FileNotFoundError):
                    pass
            except (ValueError, OSError) as e:
                logger.warning(f"Could not read PID file: {e}")
            try:
                os.remove(PID_FILE)
            except OSError:
                pass

        # Fallback: use pkill to catch any dsd-fme processes we don't know about
        try:
            result = subprocess.run(
                ["pgrep", "-f", "dsd-fme"],
                capture_output=True, text=True
            )
            if result.returncode == 0:
                pids = result.stdout.strip().split("\n")
                for pid_str in pids:
                    try:
                        pid = int(pid_str)
                        logger.warning(f"Killing orphaned dsd-fme process (PID {pid})")
                        os.kill(pid, signal.SIGTERM)
                    except (ValueError, ProcessLookupError):
                        pass
                time.sleep(2)
        except FileNotFoundError:
            pass

    def start(self):
        """Start the radio monitoring process."""
        if self.is_running():
            logger.warning("Radio process is already running")
            return

        # Kill any orphaned dsd-fme processes that may be holding the USB device
        self._kill_orphaned_processes()

        try:
            command = self._build_command()
            logger.info(f"Starting radio process: {' '.join(command)}")

            # Open log file for dsd-fme stderr output (contains main output)
            log_file = open("dsd-fme.jsonl", "a")

            # Start the process in its own process group so we can kill it
            # and all its children cleanly on shutdown
            # Note: dsd-fme outputs to stderr, not stdout
            self.process = subprocess.Popen(
                command,
                stdout=subprocess.PIPE,
                stderr=log_file,
                text=True,
                preexec_fn=os.setsid
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
            self._stopped_since = None  # Clear stopped timer on successful start

            # Write PID file so we can find orphans after worker restart
            try:
                with open(PID_FILE, "w") as f:
                    f.write(str(self.process.pid))
            except OSError as e:
                logger.warning(f"Could not write PID file: {e}")

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

            # Try graceful shutdown of the entire process group
            try:
                os.killpg(os.getpgid(self.process.pid), signal.SIGTERM)
            except ProcessLookupError:
                pass  # Process already gone

            # Wait up to 5 seconds for graceful shutdown
            try:
                self.process.wait(timeout=5)
                logger.info("Radio process stopped gracefully")
            except subprocess.TimeoutExpired:
                # Force kill the entire process group if it doesn't stop gracefully
                logger.warning("Radio process did not stop gracefully, force killing")
                try:
                    os.killpg(os.getpgid(self.process.pid), signal.SIGKILL)
                except ProcessLookupError:
                    pass
                self.process.wait()
                logger.info("Radio process force killed")

            # Close log file if it exists
            if hasattr(self, '_log_file') and self._log_file:
                self._log_file.close()
                self._log_file = None

            self.process = None
            self._last_start_time = None

            # Clean up PID file
            try:
                if os.path.exists(PID_FILE):
                    os.remove(PID_FILE)
            except OSError:
                pass

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

    def _is_rtlsdr_present(self) -> bool:
        """Check if an RTL-SDR USB device is visible on the bus.

        Returns:
            True if an RTL-SDR device is found, False otherwise.
        """
        try:
            usb_devices_path = "/sys/bus/usb/devices"
            if not os.path.exists(usb_devices_path):
                return True  # Can't check, assume present
            for device in os.listdir(usb_devices_path):
                vendor_file = os.path.join(usb_devices_path, device, "idVendor")
                if os.path.exists(vendor_file):
                    with open(vendor_file) as f:
                        if f.read().strip() == RTL_SDR_USB_VENDOR_ID:
                            return True
            return False
        except Exception as e:
            logger.error(f"Error checking for RTL-SDR USB device: {e}")
            return True  # Can't check, assume present to avoid unnecessary reboot

    def _reboot_system(self, reason: str):
        """Reboot the system to recover from USB controller failure."""
        logger.critical(
            f"REBOOTING SYSTEM - {reason}. "
            "USB controller has likely died and cannot recover without a reboot."
        )
        # Give logs a moment to flush
        time.sleep(2)
        try:
            subprocess.run(["sudo", "reboot"], check=True)
        except Exception as e:
            logger.critical(f"Failed to reboot: {e}. Manual intervention required!")

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
            f"Watchdog active: frozen detection after {FROZEN_TIMEOUT}s of inactivity"
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
                    logger.warning("Radio process died unexpectedly")
                    self._stopped_since = None  # Reset — crash handler takes over

                    # Check if the RTL-SDR USB device is still present
                    if not self._is_rtlsdr_present():
                        self._reboot_system(
                            "RTL-SDR USB device not found after process crash"
                        )
                        return  # Won't reach here if reboot succeeds

                    # Track crash times and check for repeated crashes
                    now = time.time()
                    self._crash_times.append(now)
                    self._crash_times = [
                        t for t in self._crash_times
                        if now - t < CRASH_WINDOW
                    ]
                    if len(self._crash_times) >= MAX_CRASHES_BEFORE_REBOOT:
                        self._reboot_system(
                            f"Process crashed {len(self._crash_times)} times in "
                            f"{CRASH_WINDOW}s"
                        )
                        return

                    # USB device still present, just restart the process
                    logger.info(
                        f"RTL-SDR USB device still present, restarting process... "
                        f"({len(self._crash_times)}/{MAX_CRASHES_BEFORE_REBOOT} crashes in window)"
                    )
                    try:
                        uptime = None
                        if self._last_start_time is not None:
                            uptime = int(time.time() - self._last_start_time)
                        models.log_restart("process crashed unexpectedly", uptime)
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
                    # Track how long we've been stuck in stopped state
                    if self._stopped_since is None:
                        self._stopped_since = time.time()
                        logger.warning("Radio process is stopped and not recovering — starting 2-minute reboot timer")
                    else:
                        stuck_duration = time.time() - self._stopped_since
                        if stuck_duration >= 120:
                            self._reboot_system(
                                f"Radio process stuck in stopped state for {stuck_duration:.0f}s"
                            )
                            return
                        logger.warning(
                            f"Radio process still stopped ({stuck_duration:.0f}s / 120s before reboot)"
                        )
                    continue

                # Process is running — clear stopped timer
                self._stopped_since = None

                # Check for frozen process
                if self._is_process_frozen():
                    if not self._is_rtlsdr_present():
                        self._reboot_system(
                            "RTL-SDR USB device not found (process frozen)"
                        )
                        return

                    # Track frozen restarts in the same crash window
                    now = time.time()
                    self._crash_times.append(now)
                    self._crash_times = [
                        t for t in self._crash_times
                        if now - t < CRASH_WINDOW
                    ]
                    if len(self._crash_times) >= MAX_CRASHES_BEFORE_REBOOT:
                        self._reboot_system(
                            f"Process failed {len(self._crash_times)} times in "
                            f"{CRASH_WINDOW}s (frozen)"
                        )
                        return

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
            uptime = None
            if self._last_start_time is not None:
                uptime = int(time.time() - self._last_start_time)
            models.log_restart(reason, uptime)
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

    def record_message(self):
        """Record that a message was received from the radio."""
        self._last_message_time = time.time()

    def get_status(self) -> dict:
        """Get the current status of the radio process.

        Returns:
            Dictionary with status information
        """
        is_running = self.is_running()
        uptime = None
        if is_running and self._last_start_time is not None:
            uptime = int(time.time() - self._last_start_time)

        last_message_ago = None
        if self._last_message_time is not None:
            last_message_ago = int(time.time() - self._last_message_time)

        status = {
            "running": is_running,
            "pid": self.process.pid if is_running else None,
            "uptime_seconds": uptime,
            "last_message_seconds": last_message_ago,
            "config": {
                "frequency": self.config["frequency"],
                "gain": self.config["gain"],
                "device_index": self.config.get("device_index", 0),
                "ppm": self.config.get("ppm", 0),
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


def record_radio_message():
    """Record that a message was received from the radio."""
    manager = get_radio_manager()
    manager.record_message()
