import logging
import sys

from pythonjsonlogger.json import JsonFormatter

_LEVEL_MAP = {
    "debug": logging.DEBUG,
    "info": logging.INFO,
    "warn": logging.WARNING,
    "error": logging.ERROR,
}


def setup_logging(level: str = "info") -> None:
    if level not in _LEVEL_MAP:
        raise ValueError(f"invalid log level {level!r}: must be one of debug/info/warn/error")

    handler = logging.StreamHandler(sys.stderr)
    handler.setFormatter(
        JsonFormatter(
            "{asctime}{levelname}{name}{message}",
            style="{",
            rename_fields={"asctime": "timestamp", "levelname": "level"},
        )
    )

    root = logging.getLogger()
    root.handlers = [handler]  # replace any prior handlers — idempotent
    root.setLevel(_LEVEL_MAP[level])
