"""Notification service for alert keywords."""
import logging
import requests

from app.config import get_config

logger = logging.getLogger(__name__)

_notification_config_cache = None


def _load_notification_config() -> dict:
    """Load notification configuration from YAML file with caching."""
    global _notification_config_cache
    if _notification_config_cache is None:
        try:
            config = get_config()
            _notification_config_cache = config.get("notifications", {})
        except Exception as e:
            logger.warning(f"Failed to load config.yaml for notifications: {e}")
            _notification_config_cache = {
                "groupme": {"enabled": False, "bot_id": None},
                "discord": {"enabled": False, "webhook_url": None},
                "wordlists": {"standard": {"words": []}, "strict": {"words": [], "min_occurrences": 2}}
            }
    return _notification_config_cache


def check_string(string, targetedWords):
    """Check if any target words appear in the string."""
    lower_string = string.lower()
    for word in targetedWords:
        if word.lower() in lower_string:
            return True
    return False


def check_string_min_occurrences(string, targetedWords, min_occurrences=2):
    """Check if target words appear a minimum number of times."""
    lower_string = string.lower()
    for word in targetedWords:
        if lower_string.count(word.lower()) >= min_occurrences:
            return True
    return False


def send_groupme_message(bot_id, message, unit_name):
    """Send message to GroupMe."""
    if bot_id is None:
        logger.info("No GroupMe bot ID configured")
        return
    try:
        final_message = f"{message}\n\n[From: {unit_name}]"
        body = {"text": final_message, "bot_id": bot_id}
        requests.post("https://api.groupme.com/v3/bots/post", json=body, timeout=5)
        logger.info(f"GroupMe notification sent for unit: {unit_name}")
    except Exception as e:
        logger.error(f"GroupMe notification failed: {e}")


def send_discord_message(webhook_url, message, unit_name):
    """Send message to Discord via webhook."""
    if webhook_url is None:
        logger.info("No Discord webhook URL configured")
        return
    try:
        final_message = f"{message}\n\n[From: {unit_name}]"
        body = {"content": final_message}
        requests.post(webhook_url, json=body, timeout=5)
        logger.info(f"Discord notification sent for unit: {unit_name}")
    except Exception as e:
        logger.error(f"Discord notification failed: {e}")


def check_transcript_for_alerts(message, unit_name):
    """Check transcript against alert keywords and send notifications."""
    config = _load_notification_config()

    # Get word lists from config
    wordlists = config.get("wordlists", {})
    standard_wordlist = wordlists.get("standard", {}).get("words", [])
    strict_wordlist_config = wordlists.get("strict", {})
    strict_wordlist = strict_wordlist_config.get("words", [])
    min_occurrences = strict_wordlist_config.get("min_occurrences", 2)

    # Check if message matches any alert criteria
    alert_triggered = (
        check_string(message, standard_wordlist) or
        check_string_min_occurrences(message, strict_wordlist, min_occurrences)
    )

    if not alert_triggered:
        return

    # Send notifications based on enabled services
    groupme_config = config.get("groupme", {})
    if groupme_config.get("enabled", False):
        bot_id = groupme_config.get("bot_id")
        send_groupme_message(bot_id, message, unit_name)

    discord_config = config.get("discord", {})
    if discord_config.get("enabled", False):
        webhook_url = discord_config.get("webhook_url")
        send_discord_message(webhook_url, message, unit_name)
