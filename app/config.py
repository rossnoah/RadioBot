"""Application configuration."""
import os
import yaml
import logging

from app.config_migrations import migrate_config

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
                _config_cache = yaml.safe_load(f) or {}
            _config_cache = migrate_config(_config_cache, config_path)
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

# Test console password (optional; legacy prank_password is renamed to this
# by the v2 config migration)
TEST_PAGE_PASSWORD = config.get("application", {}).get("test_password", "gotcha")

# API credentials
DEEPGRAM_API_KEY = config.get("apis", {}).get("deepgram_api_key")
if not DEEPGRAM_API_KEY:
    raise ValueError("apis.deepgram_api_key is not set in config.yaml")

# Key terms Deepgram should listen for (Keyterm Prompting). Each entry is one
# term; a multi-word entry is boosted as a single phrase. Deepgram accepts at
# most 100 per request.
MAX_DEEPGRAM_KEYTERMS = 100
DEEPGRAM_KEYTERMS = config.get("apis", {}).get("deepgram_keyterms") or []
if not isinstance(DEEPGRAM_KEYTERMS, list):
    raise ValueError("apis.deepgram_keyterms must be a list of terms")
DEEPGRAM_KEYTERMS = [str(term).strip() for term in DEEPGRAM_KEYTERMS]
if any(not term for term in DEEPGRAM_KEYTERMS):
    raise ValueError("apis.deepgram_keyterms contains an empty entry")
if len(DEEPGRAM_KEYTERMS) > MAX_DEEPGRAM_KEYTERMS:
    raise ValueError(
        f"apis.deepgram_keyterms lists {len(DEEPGRAM_KEYTERMS)} terms; "
        f"Deepgram allows at most {MAX_DEEPGRAM_KEYTERMS}"
    )


# Radio settings validation
radio_config = config.get("radio", {})
if not radio_config.get("frequency"):
    raise ValueError("radio.frequency is not set in config.yaml")
if radio_config.get("gain") is None:
    raise ValueError("radio.gain is not set in config.yaml")


