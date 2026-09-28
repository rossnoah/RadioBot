"""Notification service for alert keywords."""
import logging
import re
from datetime import datetime

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


# Dispatch reads out the current time in 24-hour form ("2254", "10-4 2251"),
# which contains numeric keywords like "225". A number is treated as the time,
# and ignored, when it reads as a clock time within this many minutes of the
# recording.
TIME_MATCH_MARGIN_MINUTES = 1

# "10-4" run into the time by the transcriber: "1042251", "42258", "10 4225"
_TEN_FOUR_PREFIXES = ("104", "4")


def _clock_minutes(digits):
    """Minutes past midnight for an HMM/HHMM string, or None if not a time."""
    if len(digits) not in (3, 4):
        return None
    hours, minutes = int(digits[:-2]), int(digits[-2:])
    if hours > 23 or minutes > 59:
        return None
    return hours * 60 + minutes


def _is_spoken_time(number, recorded_at):
    """Whether a number from the transcript is dispatch saying the current time."""
    if recorded_at is None:
        return False
    recorded = recorded_at.hour * 60 + recorded_at.minute

    candidates = [number]
    for prefix in _TEN_FOUR_PREFIXES:
        if number.startswith(prefix):
            candidates.append(number[len(prefix):])

    for candidate in candidates:
        spoken = _clock_minutes(candidate)
        if spoken is None:
            continue
        times = [spoken]
        # "fifteen" is often transcribed as "fifty" (22:15 -> "2250")
        if spoken % 60 == 50:
            times.append(spoken - 35)
        for time in times:
            diff = abs(time - recorded)
            if min(diff, 1440 - diff) <= TIME_MATCH_MARGIN_MINUTES:
                return True
    return False


def count_keyword(string, word, recorded_at=None):
    """Count occurrences of a keyword in the string.

    Text keywords match as case-insensitive substrings. Numeric keywords match
    anywhere inside a number, except numbers that are the spoken current time.
    """
    if not word.isdigit():
        return string.lower().count(word.lower())
    count = 0
    for number in re.findall(r"\d+", string):
        if word in number and not _is_spoken_time(number, recorded_at):
            count += number.count(word)
    return count


def check_string(string, targetedWords, recorded_at=None):
    """Check if any target words appear in the string."""
    return any(count_keyword(string, word, recorded_at) > 0 for word in targetedWords)


def check_string_min_occurrences(string, targetedWords, min_occurrences=2, recorded_at=None):
    """Check if target words appear a minimum number of times."""
    return any(
        count_keyword(string, word, recorded_at) >= min_occurrences
        for word in targetedWords
    )


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


def check_transcript_for_alerts(message, unit_name, recorded_at: datetime | None = None):
    """Check transcript against alert keywords and send notifications.

    recorded_at is when the transmission was recorded; without it, numbers that
    are the spoken time can't be told apart from real keyword hits.
    """
    config = _load_notification_config()

    # Get word lists from config
    wordlists = config.get("wordlists", {})
    standard_wordlist = wordlists.get("standard", {}).get("words", [])
    strict_wordlist_config = wordlists.get("strict", {})
    strict_wordlist = strict_wordlist_config.get("words", [])
    min_occurrences = strict_wordlist_config.get("min_occurrences", 2)

    # Check if message matches any alert criteria
    alert_triggered = (
        check_string(message, standard_wordlist, recorded_at) or
        check_string_min_occurrences(message, strict_wordlist, min_occurrences, recorded_at)
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
