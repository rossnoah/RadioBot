"""Application configuration."""
import os
import yaml
import logging

logger = logging.getLogger(__name__)

# Global config cache
_config_cache = None


def _load_config() -> dict:
    """Load configuration from YAML file with caching."""
    global _config_cache
    if _config_cache is None:
        config_path = os.path.join(os.path.dirname(os.path.dirname(__file__)), "config.yaml")
        try:
            with open(config_path, 'r') as f:
                _config_cache = yaml.safe_load(f)
        except FileNotFoundError:
            raise FileNotFoundError(
                f"Configuration file not found at {config_path}. "
                "Please create config.yaml from config.yaml.example"
            )
        except yaml.YAMLError as e:
            raise ValueError(f"Invalid YAML in config.yaml: {e}")
    return _config_cache


def get_config() -> dict:
    """Get the full configuration dictionary."""
    return _load_config()


# Application settings
config = _load_config()
APP_PASSWORD = config.get("application", {}).get("password")
if not APP_PASSWORD:
    raise ValueError("application.password is not set in config.yaml")

# Branding (with default)
BRANDING = config.get("application", {}).get("branding", "Radio Bot")


# API credentials
DEEPGRAM_API_KEY = config.get("apis", {}).get("deepgram_api_key")
if not DEEPGRAM_API_KEY:
    raise ValueError("apis.deepgram_api_key is not set in config.yaml")


# Radio settings validation
radio_config = config.get("radio", {})
if not radio_config.get("frequency"):
    raise ValueError("radio.frequency is not set in config.yaml")
if radio_config.get("gain") is None:
    raise ValueError("radio.gain is not set in config.yaml")


