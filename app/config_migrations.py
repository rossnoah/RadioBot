"""Config file versioning and auto-migration.

config.yaml carries a top-level `config_version` field. A config without one
is treated as version 1 (the original, unversioned schema). On startup, any
config older than CONFIG_VERSION is migrated one step at a time in memory,
the original file is backed up alongside as config.yaml.bak.v<N>, and the
migrated config is written back to disk.

The rewrite goes through yaml.safe_dump, so hand-written comments in
config.yaml are not preserved — they survive in the .bak file.

To add a migration:
  1. Bump CONFIG_VERSION.
  2. Write a `_migrate_to_<N>(config)` function that mutates the dict,
     transforming a version N-1 config into a version N config.
  3. Register it in MIGRATIONS under key N.
"""
import logging
import os
import shutil

import yaml

logger = logging.getLogger(__name__)

CONFIG_VERSION = 2


def _migrate_to_2(config: dict):
    """Rename application.prank_password -> application.test_password (the /prank
    page became the /test console)."""
    application = config.get("application")
    if isinstance(application, dict) and "prank_password" in application:
        value = application.pop("prank_password")
        application.setdefault("test_password", value)


MIGRATIONS = {
    2: _migrate_to_2,
}


def migrate_config(config: dict, config_path: str) -> dict:
    """Migrate a loaded config dict to CONFIG_VERSION, persisting the result.

    Always returns a config at the latest version (even if the disk write
    fails, the in-memory dict is fully migrated). Never raises.
    """
    current = config.get("config_version", 1)

    if current == CONFIG_VERSION:
        return config
    if current > CONFIG_VERSION:
        logger.warning(
            f"config.yaml is version {current}, newer than this code supports "
            f"({CONFIG_VERSION}); using it as-is"
        )
        return config

    # Preserve the pre-migration file before touching anything.
    backup_path = f"{config_path}.bak.v{current}"
    try:
        if not os.path.exists(backup_path):
            shutil.copy2(config_path, backup_path)
    except OSError as e:
        logger.warning(f"Could not back up config before migration: {e}")

    for version in range(current + 1, CONFIG_VERSION + 1):
        migration = MIGRATIONS.get(version)
        if migration:
            migration(config)
        logger.info(f"Migrated config from version {version - 1} to {version}")

    # Rebuild with config_version first so it's visible at the top of the file.
    config.pop("config_version", None)
    config = {"config_version": CONFIG_VERSION, **config}

    try:
        tmp_path = config_path + ".tmp"
        with open(tmp_path, 'w') as f:
            f.write("# Managed by RadioBot config migrations — see config.yaml.example for docs.\n")
            yaml.safe_dump(config, f, sort_keys=False, default_flow_style=False, allow_unicode=True)
        os.replace(tmp_path, config_path)
        logger.info(f"Wrote migrated config (version {CONFIG_VERSION}) to {config_path}")
    except OSError as e:
        logger.warning(f"Could not write migrated config to disk: {e}; continuing with in-memory config")

    return config
