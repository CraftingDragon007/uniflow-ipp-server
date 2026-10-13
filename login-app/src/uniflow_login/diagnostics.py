from __future__ import annotations

import logging
import os
import re
import stat
import traceback
from pathlib import Path
from urllib.parse import urlsplit


LOGGER_NAME = "uniflow_login"
FORMAT = "%(asctime)s %(levelname)s %(name)s: %(message)s"


def report_uncaught_exception(exc_type, exc_value, tb) -> None:
    frames = traceback.extract_tb(tb)
    stack = " > ".join(f"{frame.name}:{frame.lineno}" for frame in frames)
    logging.getLogger(LOGGER_NAME).critical(
        "Unhandled Python exception type=%s stack=%s (exception text suppressed)", exc_type.__name__, stack,
    )


def safe_origin(value: str) -> str:
    try:
        parsed = urlsplit(value)
        if parsed.scheme not in ("http", "https"):
            return "[internal or non-HTTP page]"
        host = parsed.hostname or ""
        if not re.fullmatch(r"[a-zA-Z0-9.-]+", host):
            return "[unavailable origin]"
        port = parsed.port
        return f"{parsed.scheme}://{host}" + (f":{port}" if port else "")
    except ValueError:
        return "[unavailable origin]"


def console_summary(message: str) -> str:
    # Never retain provider text: even error messages can contain OAuth secrets.
    categories = (
        "TypeError", "ReferenceError", "SyntaxError", "RangeError",
        "is not a function", "is not defined", "Content Security Policy",
        "Failed to load resource",
    )
    found = [category for category in categories if category in message]
    network_error = re.search(r"\bnet::ERR_[A-Z_]+\b", message)
    if network_error:
        found.append(network_error.group())
    handlers = (
        "smartClientMacAuthenticationData", "smartClientMacRegistrationFailed",
        "smartClientMacOpenInSystemBrowser", "SmartClientOnRegistrationSuccess",
        "SmartClientOnRegistrationError", "webChannelTransport", "QWebChannel",
    )
    found.extend(name for name in handlers if name in message)
    return ", ".join(found) if found else "provider message (text suppressed)"


def configure_logging(debug: bool = False, log_file: Path | None = None) -> None:
    logger = logging.getLogger(LOGGER_NAME)
    logger.setLevel(logging.DEBUG if debug else logging.INFO)
    logger.propagate = False
    formatter = logging.Formatter(FORMAT, datefmt="%H:%M:%S")
    handlers = [logging.StreamHandler()]
    if log_file is not None:
        fd = os.open(log_file, os.O_WRONLY | os.O_APPEND | os.O_CREAT | os.O_NOFOLLOW | os.O_NONBLOCK, 0o600)
        try:
            if not stat.S_ISREG(os.fstat(fd).st_mode):
                raise OSError("Log destination must be a regular file.")
            os.fchmod(fd, 0o600)
            stream = os.fdopen(fd, "a", encoding="utf-8")
        except OSError:
            os.close(fd)
            raise
        file_handler = logging.FileHandler(log_file, mode="a", encoding="utf-8", delay=True)
        file_handler.stream = stream
        handlers.append(file_handler)
    for handler in logger.handlers[:]:
        logger.removeHandler(handler)
        handler.close()
    for handler in handlers:
        handler.setFormatter(formatter)
        logger.addHandler(handler)
