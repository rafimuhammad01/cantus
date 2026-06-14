import json
import logging

import pytest

from logging_config import setup_logging


@pytest.fixture(autouse=True)
def _reset_logging():
    # Capture pre-test state
    root = logging.getLogger()
    saved_handlers = root.handlers[:]
    saved_level = root.level
    yield
    # Restore
    root.handlers = saved_handlers
    root.level = saved_level


@pytest.mark.parametrize(
    "level,expected_logging_level_int",
    [
        ("debug", logging.DEBUG),
        ("info", logging.INFO),
        ("warn", logging.WARNING),
        ("error", logging.ERROR),
    ],
    ids=["debug", "info", "warn", "error"],
)
def test_setup_logging_valid_levels(level, expected_logging_level_int):
    setup_logging(level)
    root = logging.getLogger()
    assert root.level == expected_logging_level_int


@pytest.mark.parametrize(
    "bad_level,expected_substr",
    [
        ("TRACE", "TRACE"),
        ("verbose", "verbose"),
        ("", ""),
    ],
    ids=["uppercase-info", "unknown-verbose", "empty"],
)
def test_setup_logging_invalid_level_raises(bad_level, expected_substr):
    with pytest.raises(ValueError) as exc:
        setup_logging(bad_level)
    assert expected_substr in str(exc.value)


@pytest.mark.parametrize(
    "call_count",
    [
        (2,),
    ],
    ids=["called-twice"],
)
def test_setup_logging_idempotent(call_count):
    (n,) = call_count
    for _ in range(n):
        setup_logging("info")
    assert len(logging.getLogger().handlers) == 1


@pytest.mark.parametrize(
    "message,extra,expected_message_field",
    [
        ("first message", None, "first message"),
        ("with extra", {"request_id": "abc-123"}, "with extra"),
    ],
    ids=["plain", "with-extra-fields"],
)
def test_log_output_is_valid_json_with_expected_fields(
    capsys, message, extra, expected_message_field
):
    setup_logging("info")
    logging.getLogger("test").info(message, extra=extra or {})
    captured = capsys.readouterr()
    lines = [line for line in captured.err.strip().splitlines() if line.strip()]
    assert lines, "No output captured on stderr"
    parsed = json.loads(lines[-1])
    assert parsed["message"] == expected_message_field
    assert parsed["level"] == "INFO"
    assert parsed.get("timestamp", "") != ""
    assert parsed["name"] == "test"
    if extra:
        for k, v in extra.items():
            assert parsed[k] == v
