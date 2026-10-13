from __future__ import annotations

import argparse
import logging
import re
import shlex
import sys
from pathlib import Path

from PySide6.QtCore import QObject, QThread, QTimer, Qt, QUrl, Signal, qVersion
from PySide6.QtGui import QDesktopServices
from PySide6.QtWebEngineCore import QWebEnginePage, QWebEngineProfile, QWebEngineSettings
from PySide6.QtWebEngineWidgets import QWebEngineView
from PySide6.QtWidgets import (
    QApplication, QCheckBox, QFileDialog, QHBoxLayout, QLabel, QLineEdit,
    QMainWindow, QMessageBox, QPlainTextEdit, QPushButton, QVBoxLayout, QWidget,
)
from shiboken6 import isValid

from .browser import RegistrationPage, RequestLogger
from .diagnostics import FORMAT, LOGGER_NAME, configure_logging, report_uncaught_exception
from .export import export_bundle
from .protocol import Credentials, LoginError, LoginResult, OAuthClient, RegistrationSession, Tenant


logger = logging.getLogger(__name__)


class LogEmitter(QObject):
    line = Signal(str)


class WindowLogHandler(logging.Handler):
    def __init__(self, window):
        super().__init__()
        self.emitter = LogEmitter(window)
        self.emitter.line.connect(window.logs.appendPlainText)
        self.setFormatter(logging.Formatter(FORMAT, datefmt="%H:%M:%S"))

    def emit(self, record):
        self.emitter.line.emit(self.format(record))


class TokenRequest(QThread):
    completed = Signal(object)
    failed = Signal(str)

    def __init__(self, credentials: Credentials, parent=None):
        super().__init__(parent)
        self.credentials = credentials

    def run(self):
        try:
            if self.isInterruptionRequested():
                return
            result = OAuthClient().exchange(self.credentials)
            if not self.isInterruptionRequested():
                self.completed.emit(result)
        except LoginError as exc:
            if not self.isInterruptionRequested():
                self.failed.emit(str(exc))
        finally:
            self.credentials = None


class LoginWindow(QMainWindow):
    def __init__(self, plist: Path | None = None):
        super().__init__()
        self.setWindowTitle("uniFLOW Login")
        self.resize(1100, 850)
        self.setAttribute(Qt.WidgetAttribute.WA_DeleteOnClose)
        self.tenant: Tenant | None = None
        self.session: RegistrationSession | None = None
        self.result: LoginResult | None = None
        self.thread: TokenRequest | None = None
        self.profile = None
        self.popups = []
        self.closing = False
        self.callback_consumed = False
        self.diagnostic_timer = QTimer(self)
        self.diagnostic_timer.setInterval(30_000)
        self.diagnostic_timer.timeout.connect(self.log_browser_diagnostics)

        self.import_button = QPushButton("Open tenant plist...")
        self.import_button.clicked.connect(self.choose_plist)
        self.login_button = QPushButton("Log in")
        self.login_button.setEnabled(False)
        self.login_button.clicked.connect(self.begin_login)
        self.cancel_button = QPushButton("Cancel login")
        self.cancel_button.setEnabled(False)
        self.cancel_button.clicked.connect(self.cancel_login)
        self.export_button = QPushButton("Export server configuration...")
        self.export_button.setEnabled(False)
        self.export_button.clicked.connect(self.export_config)
        self.diagnostics_button = QPushButton("Log browser diagnostics")
        self.diagnostics_button.clicked.connect(self.log_browser_diagnostics)
        toolbar = QHBoxLayout()
        for button in (self.import_button, self.login_button, self.cancel_button, self.export_button, self.diagnostics_button):
            toolbar.addWidget(button)
        self.tenant_label = QLabel("Select the .tenantcfg.plist from your SmartClient installer.")
        self.tenant_label.setTextFormat(Qt.TextFormat.PlainText)
        self.management = QLineEdit()
        self.management.setPlaceholderText("Management-server hostname (defaults to the plist tenant)")
        self.system_service = QCheckBox("Export paths for the packaged system service (requires manual installation)")
        self.status = QLabel("Login cookies are kept in memory only. Configuration exports contain secrets.")
        self.status.setTextFormat(Qt.TextFormat.PlainText)
        self.status.setWordWrap(True)
        self.browser = QWebEngineView()
        self.instructions = QPlainTextEdit()
        self.instructions.setReadOnly(True)
        self.instructions.setMaximumHeight(150)
        self.instructions.hide()
        self.logs = QPlainTextEdit()
        self.logs.setReadOnly(True)
        self.logs.setMaximumHeight(160)
        self.logs.setMaximumBlockCount(1000)
        self.logs.setPlaceholderText("Login diagnostics (credentials and provider console text are suppressed)")
        layout = QVBoxLayout()
        layout.addLayout(toolbar)
        layout.addWidget(self.tenant_label)
        layout.addWidget(self.management)
        layout.addWidget(self.system_service)
        layout.addWidget(self.status)
        layout.addWidget(self.browser, 1)
        layout.addWidget(self.instructions)
        layout.addWidget(self.logs)
        root = QWidget()
        root.setLayout(layout)
        self.setCentralWidget(root)
        self.log_handler = WindowLogHandler(self)
        logging.getLogger(LOGGER_NAME).addHandler(self.log_handler)
        logger.info("Login diagnostics active; Qt=%s; authentication material is never logged", qVersion())
        if plist:
            self.load_plist(plist)

    def choose_plist(self):
        path, _ = QFileDialog.getOpenFileName(self, "Open tenant plist", "", "Property lists (*.plist);;All files (*)")
        if path:
            self.load_plist(Path(path))

    def load_plist(self, path: Path):
        try:
            tenant = Tenant.load(path)
        except LoginError as exc:
            self.show_error(str(exc))
            return
        self.cancel_login()
        self.tenant = tenant
        logger.info("Tenant plist loaded successfully")
        self.management.setText(tenant.domain)
        self.tenant_label.setText(f"{tenant.installer_name} | {tenant.domain} | {tenant.product_version}")
        self.login_button.setEnabled(True)
        self.status.setText("Plist loaded. Click Log in to open this tenant's registration page.")

    def show_error(self, message: str):
        logger.error("%s", message)
        self.status.setText(message)
        QMessageBox.critical(self, "uniFLOW Login", message)

    def begin_login(self):
        if not self.tenant or (self.thread and self.thread.isRunning()):
            return
        self.cancel_login()
        self.session = RegistrationSession(self.tenant)
        self.callback_consumed = False
        self.result = None
        self.export_button.setEnabled(False)
        self.instructions.hide()
        self.profile = QWebEngineProfile(QApplication.instance())
        # The tenant selects Windows callbacks unless the browser identifies as Mac.
        self.profile.setHttpUserAgent(re.sub(
            r"\([^)]*\)", "(Macintosh; Intel Mac OS X 10_15_7)", self.profile.httpUserAgent(), count=1,
        ))
        self.profile.setPersistentCookiesPolicy(QWebEngineProfile.PersistentCookiesPolicy.NoPersistentCookies)
        self.profile.setHttpCacheType(QWebEngineProfile.HttpCacheType.MemoryHttpCache)
        self.request_logger = RequestLogger(self.profile)
        self.profile.setUrlRequestInterceptor(self.request_logger)
        self.profile.downloadRequested.connect(self.block_download)
        logger.info("Login started: platform=Macintosh off_record=%s", self.profile.isOffTheRecord())
        old_page = self.browser.page()
        try:
            page = self.make_page(self.browser)
        except LoginError as exc:
            self.session = None
            self.profile.deleteLater()
            self.profile = None
            self.show_error(str(exc))
            return
        self.browser.setPage(page)
        if isValid(old_page):
            old_page.deleteLater()
        self.cancel_button.setEnabled(True)
        self.status.setText("Sign in and complete MFA. The tenant may open your system browser; keep this window open.")
        self.browser.load(QUrl(self.session.registration_url()))
        self.diagnostic_timer.start()

    def block_download(self, download):
        logger.warning("Browser download blocked")
        download.cancel()

    def log_browser_diagnostics(self):
        logger.info(
            "Login state session_active=%s callback_received=%s token_request_running=%s authenticated=%s popups=%s",
            self.session is not None, self.callback_consumed,
            bool(self.thread and self.thread.isRunning()), self.result is not None, len(self.popups),
        )
        for view in [self.browser] + list(self.popups):
            page = view.page()
            if isinstance(page, RegistrationPage):
                page.log_diagnostics()

    def make_page(self, parent):
        session = self.session
        page = RegistrationPage(self.profile, self.tenant.domain, parent, self.make_popup)
        settings = page.settings()
        for option in (
            QWebEngineSettings.WebAttribute.LocalContentCanAccessFileUrls,
            QWebEngineSettings.WebAttribute.LocalContentCanAccessRemoteUrls,
            QWebEngineSettings.WebAttribute.JavascriptCanAccessClipboard,
            QWebEngineSettings.WebAttribute.AllowRunningInsecureContent,
        ):
            settings.setAttribute(option, False)
        page.bridge.authentication.connect(lambda data: self.authenticate(data) if self.session is session else None)
        page.bridge.external_browser.connect(lambda url: self.open_system_browser(url) if self.session is session else None)
        page.bridge.failure.connect(
            lambda: self.status.setText("Tenant registration failed. Restart login and try again.")
            if self.session is session else None
        )
        page.notice.connect(lambda message: self.status.setText(message) if self.session is session else None)
        page.loadFinished.connect(
            lambda success: self.status.setText("Login page could not be loaded. Check connectivity and TLS trust, then restart login.")
            if not success and self.session is session and self.result is None else None
        )
        return page

    def open_system_browser(self, url: str):
        if self.session is None or self.callback_consumed or self.closing:
            logger.warning("External-browser handoff ignored: login session is not awaiting authentication")
            return
        if not QDesktopServices.openUrl(QUrl(url)):
            self.show_error("Could not open the system browser. Configure a default HTTPS browser, then retry login.")
            return
        logger.info("System-browser handoff accepted; keeping the tenant page and registration session active")
        self.status.setText(
            "Complete login/MFA in your system browser. Keep this window open; "
            "the tenant page will deliver the registration result here."
        )

    def make_popup(self):
        if self.session is None or self.profile is None:
            return None
        popup = QWebEngineView()
        logger.info("Opening identity-provider popup")
        popup.setWindowTitle("uniFLOW identity-provider login")
        popup.resize(900, 750)
        popup.setAttribute(Qt.WidgetAttribute.WA_DeleteOnClose)
        page = self.make_page(popup)
        popup.setPage(page)
        self.popups.append(popup)
        popup.destroyed.connect(lambda: self.popups.remove(popup) if popup in self.popups else None)
        popup.show()
        return page

    def authenticate(self, data: str):
        if self.session is None or self.callback_consumed or self.closing:
            logger.warning("Authentication callback ignored: no active session, duplicate callback, or closing window")
            return
        self.callback_consumed = True
        self.diagnostic_timer.stop()
        logger.info("Decoding native registration response")
        try:
            credentials = self.session.decode_authorization(data)
        except LoginError as exc:
            self.registration_finished(False)
            self.show_error(str(exc))
            return
        logger.info("Registration response decrypted and validated")
        self.status.setText("Exchanging the tenant-issued authorization code for OAuth tokens...")
        self.import_button.setEnabled(False)
        self.login_button.setEnabled(False)
        self.thread = TokenRequest(credentials, self)
        self.thread.completed.connect(self.authenticated)
        self.thread.failed.connect(self.token_error)
        self.thread.finished.connect(self.request_finished)
        self.thread.start()

    def registration_finished(self, success: bool):
        function = "SmartClientOnRegistrationSuccess" if success else "SmartClientOnRegistrationError"
        script = f"if (typeof {function} === 'function') {function}();"
        for view in [self.browser] + list(self.popups):
            view.page().runJavaScript(script)

    def authenticated(self, result: LoginResult):
        if self.session is None or self.closing:
            return
        self.result = result
        logger.info("OAuth login succeeded; configuration export is available")
        self.registration_finished(True)
        self.export_button.setEnabled(True)
        self.cancel_button.setEnabled(False)
        self.status.setText("OAuth login succeeded. Export private server configuration and tokens.")

    def token_error(self, message: str):
        if self.closing or self.session is None:
            return
        self.registration_finished(False)
        self.show_error(message)

    def request_finished(self):
        self.import_button.setEnabled(True)
        self.login_button.setEnabled(self.tenant is not None)
        self.thread.deleteLater()
        self.thread = None
        if self.closing:
            self.close()

    def cancel_login(self):
        if self.session is not None:
            logger.info("Login cancelled; clearing in-memory browser session")
        self.diagnostic_timer.stop()
        self.session = None
        self.result = None
        self.browser.stop()
        if self.thread and self.thread.isRunning():
            self.thread.requestInterruption()
        self.export_button.setEnabled(False)
        self.cancel_button.setEnabled(False)
        for popup in list(self.popups):
            popup.close()
        self.popups.clear()
        if self.profile is not None:
            page = self.browser.page()
            self.browser.setPage(QWebEnginePage(self.browser))
            if isValid(page):
                page.deleteLater()
            self.profile.deleteLater()
            self.profile = None

    def export_config(self):
        if self.result is None or self.tenant is None:
            return
        parent = QFileDialog.getExistingDirectory(self, "Choose parent directory for a new private configuration bundle")
        if not parent:
            return
        try:
            directory = export_bundle(
                self.tenant, self.result, Path(parent), self.management.text(), self.system_service.isChecked(),
            )
        except LoginError as exc:
            self.show_error(str(exc))
            return
        quote = lambda path: shlex.quote(str(path))
        if self.system_service.isChecked():
            instructions = "\n".join((
                "Install the server package first, then run:",
                "sudo install -d -o uniflow-ipp -g uniflow-ipp -m 0700 /var/lib/uniflow-ipp",
                f"sudo install -o root -g uniflow-ipp -m 0640 {quote(directory / 'config.toml')} /etc/uniflow-ipp/config.toml",
                f"sudo install -o uniflow-ipp -g uniflow-ipp -m 0600 {quote(directory / 'token.json')} /var/lib/uniflow-ipp/token.json",
                "sudo systemctl restart uniflow-ipp.service",
            ))
        else:
            instructions = f"Run the server as your current user:\nuniflow-ipp-server --config {quote(directory / 'config.toml')}"
        self.instructions.setPlainText(instructions)
        self.instructions.show()
        logger.info("Private configuration bundle exported; system_service=%s", self.system_service.isChecked())
        self.status.setText(f"Exported config.toml and token.json to {directory}. Both files are mode 0600.")

    def closeEvent(self, event):
        self.cancel_login()
        if self.thread and self.thread.isRunning():
            self.closing = True
            self.status.setText("Cancelling login; waiting for the token request to finish safely...")
            event.ignore()
            return
        event.accept()
        logging.getLogger(LOGGER_NAME).removeHandler(self.log_handler)
        self.log_handler.close()


def main():
    parser = argparse.ArgumentParser(description="Qt WebEngine uniFLOW tenant login and server configuration")
    parser.add_argument("--plist", type=Path, help="tenant .plist from the SmartClient installer")
    parser.add_argument("--debug", action="store_true", help="verbose, credential-safe browser diagnostics")
    parser.add_argument("--log-file", type=Path, help="append diagnostics to a private mode-0600 file")
    args = parser.parse_args()
    try:
        configure_logging(args.debug, args.log_file)
    except OSError:
        parser.error("Cannot open the log file. Choose a writable regular file, not a symlink.")
    sys.excepthook = report_uncaught_exception
    app = QApplication([sys.argv[0]])
    window = LoginWindow(args.plist)
    window.show()
    return app.exec()
