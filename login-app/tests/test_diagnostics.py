import logging
import tempfile
import unittest
from pathlib import Path

from uniflow_login.diagnostics import LOGGER_NAME, configure_logging, console_summary, report_uncaught_exception, safe_origin


class DiagnosticsTests(unittest.TestCase):
    def test_origins_hide_authentication_material(self):
        self.assertEqual(
            safe_origin("https://username:password@tenant.example.invalid/private-secret?code=secret#token-secret"),
            "https://tenant.example.invalid",
        )
        for value in ("data:text/html,secret", "file:///private-secret", "https://tenant.invalid:secret"):
            self.assertNotIn("secret", safe_origin(value))

    def test_console_text_is_never_retained(self):
        summary = console_summary("TypeError: smartClientMacAuthenticationData is not a function; access_token=private-secret")
        self.assertIn("TypeError", summary)
        self.assertIn("smartClientMacAuthenticationData", summary)
        self.assertNotIn("private-secret", summary)
        self.assertEqual(console_summary("private-secret"), "provider message (text suppressed)")

    def test_uncaught_exception_is_reported_without_its_contents(self):
        try:
            raise ValueError("private-secret")
        except ValueError as exc:
            with self.assertLogs(LOGGER_NAME, level="CRITICAL") as captured:
                report_uncaught_exception(type(exc), exc, exc.__traceback__)
        output = "\n".join(captured.output)
        self.assertIn("ValueError", output)
        self.assertIn("test_uncaught_exception_is_reported_without_its_contents", output)
        self.assertNotIn("private-secret", output)

    def test_private_file_and_console_logging(self):
        logger = logging.getLogger(LOGGER_NAME)
        old_handlers, old_level, old_propagate = logger.handlers[:], logger.level, logger.propagate
        logger.handlers.clear()
        try:
            with tempfile.TemporaryDirectory() as temp:
                path = Path(temp) / "login.log"
                configure_logging(debug=True, log_file=path)
                logger.debug("safe debug event")
                self.assertIn("safe debug event", path.read_text())
                self.assertEqual(path.stat().st_mode & 0o777, 0o600)
                link = Path(temp) / "link.log"
                link.symlink_to(path)
                with self.assertRaises(OSError):
                    configure_logging(log_file=link)
                self.assertNotIn("secret", path.read_text())
                for handler in logger.handlers:
                    handler.close()
        finally:
            logger.handlers = old_handlers
            logger.setLevel(old_level)
            logger.propagate = old_propagate
