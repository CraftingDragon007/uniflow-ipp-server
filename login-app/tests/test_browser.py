import json
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
        self.page.bridge.authentication.connect(self.received.append)
        self.page.bridge.failure.connect(lambda: self.failures.append(True))
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


class GuiFlowTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.app = QApplication.instance() or QApplication([])
        cls.app.setQuitOnLastWindowClosed(False)

    def test_browser_to_oauth_to_server_configuration(self):
        window = LoginWindow()
        window.tenant = tenant()
        window.management.setText(tenant().domain)
        window.show()
        try:
            with patch.object(RegistrationSession, "registration_url", return_value="about:blank"), patch("uniflow_login.app.RegistrationPage", FixturePage):
                window.begin_login()
            page = window.browser.page()
            loaded = []
            page.loadFinished.connect(loaded.append)
            page.setHtml("<html><script>function SmartClientOnRegistrationSuccess(){window.fixtureSuccess=true}</script></html>", QUrl("https://tenant.example.invalid/Home/SmartClientRegistration"))
            self.assertTrue(wait_for(lambda: loaded and loaded[-1]))
            message = authorization_message(window.session)
            client = OAuthClient()
            response = Response(json.dumps({
                "access_token": "test-qt-access", "refresh_token": "test-qt-refresh",
                "token_type": "Bearer", "expires_in": 3600,
            }).encode())
            with patch("uniflow_login.app.OAuthClient", return_value=client), patch.object(client.opener, "open", return_value=response):
                page.runJavaScript(f"window.webkit.messageHandlers.smartClientMacAuthenticationData.postMessage({json.dumps(message)});")
                self.assertTrue(wait_for(lambda: window.result is not None and window.thread is None), window.status.text())
            self.assertTrue(window.export_button.isEnabled())
            self.assertEqual(window.result.token["access_token"], "test-qt-access")
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


if __name__ == "__main__":
    unittest.main()
