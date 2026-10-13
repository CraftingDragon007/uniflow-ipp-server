import json
import logging
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

from PySide6.QtCore import QCoreApplication, QEvent, QUrl
from PySide6.QtTest import QTest
from PySide6.QtWebEngineCore import QWebEngineProfile
from PySide6.QtWebEngineWidgets import QWebEngineView
from PySide6.QtWidgets import QApplication

from uniflow_login.app import LoginWindow
from uniflow_login.browser import RegistrationPage
from uniflow_login.diagnostics import LOGGER_NAME
from uniflow_login.protocol import OAuthClient, RegistrationSession
from test_protocol import Response, authorization_message, tenant


def wait_for(predicate, milliseconds=8000):
    for _ in range(milliseconds // 20):
        if predicate():
            return True
        QTest.qWait(20)
    return bool(predicate())

class FixturePage(RegistrationPage):
    def acceptNavigationRequest(self, url, navigation_type, is_main_frame):
        # setHtml's internal data URL is a controlled test fixture, not a
        # navigation mode enabled in the production login browser.
        if url.scheme() == "data":
            return True
        return super().acceptNavigationRequest(url, navigation_type, is_main_frame)


class BrowserTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.app = QApplication.instance() or QApplication([])
        cls.app.setQuitOnLastWindowClosed(False)

    def setUp(self):
        self.profile = QWebEngineProfile(self.app)
        self.view = QWebEngineView()
        self.page = FixturePage(self.profile, tenant().domain, self.view)
        self.view.setPage(self.page)
        self.received = []
        self.failures = []
        self.external_urls = []
        self.page.bridge.authentication.connect(self.received.append)
        self.page.bridge.failure.connect(lambda: self.failures.append(True))
        self.page.bridge.external_browser.connect(self.external_urls.append)
        self.view.show()

    def tearDown(self):
        self.view.close()
        self.view.deleteLater()
        QCoreApplication.sendPostedEvents(None, QEvent.Type.DeferredDelete)
        self.profile.deleteLater()
        QCoreApplication.sendPostedEvents(None, QEvent.Type.DeferredDelete)

    def load_html(self, html, base="https://tenant.example.invalid/Home/SmartClientRegistration"):
        loaded = []
        self.page.loadFinished.connect(loaded.append)
        self.page.setHtml(html, QUrl(base))
        self.assertTrue(wait_for(lambda: loaded))
        self.assertTrue(loaded[-1])
        QTest.qWait(200)

    def test_native_handler_bridge_and_off_record_profile(self):
        self.assertTrue(self.profile.isOffTheRecord())
        self.load_html("<html><body>Offline registration fixture</body></html>")
        self.page.runJavaScript(
            "window.webkit.messageHandlers.smartClientMacAuthenticationData.postMessage('test-authentication');"
        )
        self.assertTrue(wait_for(lambda: self.received), "Tenant callback did not reach native Qt WebChannel")
        self.assertEqual(self.received, ["test-authentication"])
        self.page.runJavaScript("window.webkit.messageHandlers.smartClientMacRegistrationFailed.postMessage('unused');")
        self.assertTrue(wait_for(lambda: self.failures))

    def test_registration_document_initialization_cannot_remove_bridge(self):
        self.load_html("""<html><script>
window.webkit = {};
function SmartClientOnRegistrationSuccess() {}
function SmartClientOnRegistrationError() {}
</script><body>Registration page</body></html>""")
        self.page.runJavaScript(
            "window.webkit.messageHandlers.smartClientMacAuthenticationData.postMessage('registration-after-reset');"
        )
        self.assertTrue(wait_for(lambda: self.received))
        QTest.qWait(100)
        self.assertEqual(self.received, ["registration-after-reset"])
        with self.assertLogs(LOGGER_NAME, level="INFO") as captured:
            self.page.log_diagnostics()
            self.assertTrue(wait_for(lambda: any("'authenticationHandler': True" in line for line in captured.output)))
        self.assertIn("'externalBrowserHandler': True", "\n".join(captured.output))

    def test_other_origins_and_frames_cannot_submit(self):
        self.load_html("<html><body>Identity provider</body></html>", "https://identity.example.invalid/login")
        self.page.bridge.authenticate("forged")
        self.assertEqual(self.received, [])
        values = []
        self.page.runJavaScript("typeof window.webkit", values.append)
        self.assertTrue(wait_for(lambda: values))
        self.assertEqual(values, ["undefined"])
        self.load_html("<html><body>Tenant fixture</body></html>")
        self.page.runJavaScript("""
const frame = document.createElement("iframe");
frame.sandbox = "allow-scripts";
frame.srcdoc = '<script>window.parent.postMessage({uniflowRegistration:true,kind:"authentication",data:"forged-frame"}, "*");<\\/script>';
document.body.appendChild(frame);
""")
        QTest.qWait(400)
        self.assertEqual(self.received, [])

    def test_http_and_file_navigation_are_blocked(self):
        for url in ("http://tenant.example.invalid/login", "file:///etc/passwd"):
            self.assertFalse(self.page.acceptNavigationRequest(QUrl(url), None, True))
        self.assertTrue(self.page.acceptNavigationRequest(QUrl("https://identity.example.invalid/login"), None, True))

    def test_page_probe_and_console_logging_are_safe(self):
        self.load_html("<html><script>window.PageConfig={ClientType:'SmartClientForMac'}</script></html>")
        with self.assertLogs(LOGGER_NAME, level="INFO") as captured:
            self.page.log_diagnostics()
            self.assertTrue(wait_for(lambda: any("client_type=SmartClientForMac" in line for line in captured.output)))
            self.page.runJavaScript("console.error('TypeError: private-secret?code=private-code')")
            self.assertTrue(wait_for(lambda: any("JavaScript TypeError" in line for line in captured.output)))
        output = "\n".join(captured.output)
        self.assertIn("'authenticationHandler': True", output)
        self.assertNotIn("private-secret", output)
        self.assertNotIn("private-code", output)

    def test_bridge_diagnostics_do_not_accept_arbitrary_text(self):
        with self.assertNoLogs(LOGGER_NAME):
            self.page.bridge.diagnostic("private-secret")

    def test_external_browser_capability_and_handoff(self):
        self.load_html("""<html><script>
window.PageConfig = {ClientType: "SmartClientForMac"};
window.unsupportedClient = !window.webkit || !window.webkit.messageHandlers ||
    !window.webkit.messageHandlers.smartClientMacOpenInSystemBrowser;
</script></html>""")
        values = []
        self.page.runJavaScript("window.unsupportedClient", values.append)
        self.assertTrue(wait_for(lambda: values))
        self.assertEqual(values, [False])
        url = "https://identity.example.invalid/login?code=private-handoff#private-fragment"
        with self.assertLogs(LOGGER_NAME, level="INFO") as captured:
            self.page.runJavaScript(
                f"window.webkit.messageHandlers.smartClientMacOpenInSystemBrowser.postMessage({json.dumps(url)});"
            )
            self.assertTrue(wait_for(lambda: self.external_urls))
            self.page.log_diagnostics()
            self.assertTrue(wait_for(lambda: any("'externalBrowserHandler': True" in line for line in captured.output)))
        self.assertEqual(self.external_urls, [url])
        self.assertNotIn("private-handoff", "\n".join(captured.output))
        self.assertNotIn("private-fragment", "\n".join(captured.output))

    def test_external_browser_rejects_invalid_urls_origins_and_frames(self):
        self.load_html("<html>Tenant</html>")
        for url in (
            "http://identity.example.invalid/login", "file:///etc/passwd",
            "javascript:alert(1)", "https://user:secret@identity.example.invalid/",
            "https://localhost/login", "https://identity.example.invalid:bad/",
        ):
            self.page.bridge.openInSystemBrowser(url)
        self.page.runJavaScript("""
const frame = document.createElement("iframe");
frame.sandbox = "allow-scripts";
frame.srcdoc = '<script>window.parent.postMessage({uniflowRegistration:true,kind:"external-browser",data:"https://identity.example.invalid/login"}, "*");<\\/script>';
document.body.appendChild(frame);
""")
        QTest.qWait(300)
        self.assertEqual(self.external_urls, [])
        self.load_html("<html>Provider</html>", "https://identity.example.invalid/login")
        self.page.bridge.openInSystemBrowser("https://identity.example.invalid/login")
        self.assertEqual(self.external_urls, [])


class GuiFlowTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.app = QApplication.instance() or QApplication([])
        cls.app.setQuitOnLastWindowClosed(False)

    def test_visible_diagnostics_and_waiting_state(self):
        window = LoginWindow()
        window.show()
        logger = logging.getLogger(LOGGER_NAME)
        old_level = logger.level
        logger.setLevel(logging.INFO)
        try:
            window.log_browser_diagnostics()
            self.assertTrue(wait_for(lambda: "session_active=False" in window.logs.toPlainText()))
            self.assertIn("callback_received=False", window.logs.toPlainText())
        finally:
            logger.setLevel(old_level)
            window.close()
            QCoreApplication.sendPostedEvents(None, QEvent.Type.DeferredDelete)

    def test_browser_to_oauth_to_server_configuration(self):
        logger = logging.getLogger(LOGGER_NAME)
        old_level = logger.level
        logger.setLevel(logging.INFO)
        window = LoginWindow()
        window.tenant = tenant()
        window.management.setText(tenant().domain)
        window.show()
        try:
            with patch.object(RegistrationSession, "registration_url", return_value="about:blank"), patch("uniflow_login.app.RegistrationPage", FixturePage):
                window.begin_login()
            self.assertIn("Macintosh", window.profile.httpUserAgent())
            self.assertIn("QtWebEngine/", window.profile.httpUserAgent())
            self.assertIn("Chrome/", window.profile.httpUserAgent())
            page = window.browser.page()
            loaded = []
            page.loadFinished.connect(loaded.append)
            page.setHtml("""<html><script>
window.webkit = {};
function SmartClientOnRegistrationSuccess(){window.fixtureSuccess=true}
function deliverRegistration(data) {
    // The tenant chooses its native client contract from the browser identity.
    if (navigator.userAgent.includes("Macintosh") &&
        window.webkit.messageHandlers.smartClientMacOpenInSystemBrowser) {
        window.webkit.messageHandlers.smartClientMacAuthenticationData.postMessage(data);
    } else {
        window.external.SmartClientAuthenticationData(data);
    }
}
</script></html>""", QUrl("https://tenant.example.invalid/Home/SmartClientRegistration"))
            self.assertTrue(wait_for(lambda: loaded and loaded[-1]))
            url = "https://identity.example.invalid/login?private-handoff"
            session = window.session
            with patch("uniflow_login.app.QDesktopServices.openUrl", return_value=True) as opened:
                page.runJavaScript(
                    f"window.webkit.messageHandlers.smartClientMacOpenInSystemBrowser.postMessage({json.dumps(url)});"
                )
                self.assertTrue(wait_for(lambda: opened.called))
                self.assertEqual(opened.call_args.args[0].toString(), url)
            self.assertIs(window.session, session)
            self.assertTrue(window.diagnostic_timer.isActive())
            self.assertFalse(window.callback_consumed)
            self.assertIn("system browser", window.status.text())
            message = authorization_message(window.session)
            client = OAuthClient()
            response = Response(json.dumps({
                "access_token": "test-qt-access", "refresh_token": "test-qt-refresh",
                "token_type": "private-native-token-label", "expires_in": 3600,
            }).encode())
            with patch("uniflow_login.app.OAuthClient", return_value=client), patch.object(client.opener, "open", return_value=response):
                page.runJavaScript(f"deliverRegistration({json.dumps(message)});")
                self.assertTrue(wait_for(lambda: window.result is not None and window.thread is None), window.status.text())
            self.assertTrue(window.export_button.isEnabled())
            self.assertEqual(window.result.token["access_token"], "test-qt-access")
            self.assertTrue(wait_for(lambda: "OAuth login succeeded" in window.logs.toPlainText()))
            self.assertIn("Registration response decrypted and validated", window.logs.toPlainText())
            for secret in ("test-qt-access", "test-qt-refresh", "private-native-token-label", window.result.credentials.client_secret, message):
                self.assertNotIn(secret, window.logs.toPlainText())
            self.assertNotIn("private-handoff", window.logs.toPlainText())
            self.assertEqual(window.result.token["token_type"], "Bearer")
            with tempfile.TemporaryDirectory() as temp, patch("uniflow_login.app.QFileDialog.getExistingDirectory", return_value=temp):
                window.export_config()
                bundles = list(Path(temp).iterdir())
                self.assertEqual(len(bundles), 1)
                self.assertTrue((bundles[0] / "config.toml").is_file())
                self.assertTrue((bundles[0] / "token.json").is_file())
                self.assertNotIn("test-qt-access", window.instructions.toPlainText())
            popup = window.make_popup()
            self.assertIsNotNone(popup)
            popup.parent().close()
            QCoreApplication.sendPostedEvents(None, QEvent.Type.DeferredDelete)
            self.assertEqual(window.popups, [])
        finally:
            window.close()
            QCoreApplication.sendPostedEvents(None, QEvent.Type.DeferredDelete)
            logger.setLevel(old_level)

    def test_system_browser_launch_failure_is_visible(self):
        window = LoginWindow()
        window.tenant = tenant()
        window.session = RegistrationSession(window.tenant)
        try:
            with patch("uniflow_login.app.QDesktopServices.openUrl", return_value=False), patch.object(window, "show_error") as error:
                window.open_system_browser("https://identity.example.invalid/login")
                error.assert_called_once()
                self.assertIn("Could not open the system browser", error.call_args.args[0])
            window.session = None
            with patch("uniflow_login.app.QDesktopServices.openUrl") as opened:
                window.open_system_browser("https://identity.example.invalid/login")
                opened.assert_not_called()
        finally:
            window.close()
            QCoreApplication.sendPostedEvents(None, QEvent.Type.DeferredDelete)


if __name__ == "__main__":
    unittest.main()
