from __future__ import annotations

import argparse
import shlex
import sys
from pathlib import Path

from PySide6.QtCore import QThread, Qt, QUrl, Signal
from PySide6.QtWebEngineCore import QWebEnginePage, QWebEngineProfile, QWebEngineSettings
from PySide6.QtWebEngineWidgets import QWebEngineView
from PySide6.QtWidgets import (
    QApplication, QCheckBox, QFileDialog, QHBoxLayout, QLabel, QLineEdit,
    QMainWindow, QMessageBox, QPlainTextEdit, QPushButton, QVBoxLayout, QWidget,
)
from shiboken6 import isValid

from .browser import RegistrationPage
from .export import export_bundle
from .protocol import Credentials, LoginError, LoginResult, OAuthClient, RegistrationSession, Tenant


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
        toolbar = QHBoxLayout()
        for button in (self.import_button, self.login_button, self.cancel_button, self.export_button):
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
        layout = QVBoxLayout()
        layout.addLayout(toolbar)
        layout.addWidget(self.tenant_label)
        layout.addWidget(self.management)
        layout.addWidget(self.system_service)
        layout.addWidget(self.status)
        layout.addWidget(self.browser, 1)
        layout.addWidget(self.instructions)
        root = QWidget()
        root.setLayout(layout)
        self.setCentralWidget(root)
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
        self.management.setText(tenant.domain)
        self.tenant_label.setText(f"{tenant.installer_name} | {tenant.domain} | {tenant.product_version}")
        self.login_button.setEnabled(True)
        self.status.setText("Plist loaded. Click Log in to open this tenant's registration page.")

    def show_error(self, message: str):
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
        self.profile.setPersistentCookiesPolicy(QWebEngineProfile.PersistentCookiesPolicy.NoPersistentCookies)
        self.profile.setHttpCacheType(QWebEngineProfile.HttpCacheType.MemoryHttpCache)
        self.profile.downloadRequested.connect(lambda download: download.cancel())
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
        self.status.setText("Sign in and complete any MFA inside the browser. TLS certificate verification is enabled.")
        self.browser.load(QUrl(self.session.registration_url()))

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

    def make_popup(self):
        if self.session is None or self.profile is None:
            return None
        popup = QWebEngineView()
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
            return
        self.callback_consumed = True
        try:
            credentials = self.session.decode_authorization(data)
        except LoginError as exc:
            self.registration_finished(False)
            self.show_error(str(exc))
            return
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
        self.status.setText(f"Exported config.toml and token.json to {directory}. Both files are mode 0600.")

    def closeEvent(self, event):
        self.cancel_login()
        if self.thread and self.thread.isRunning():
            self.closing = True
            self.status.setText("Cancelling login; waiting for the token request to finish safely...")
            event.ignore()
            return
        event.accept()


def main():
    parser = argparse.ArgumentParser(description="Qt WebEngine uniFLOW tenant login and server configuration")
    parser.add_argument("--plist", type=Path, help="tenant .plist from the SmartClient installer")
    args = parser.parse_args()
    app = QApplication([sys.argv[0]])
    window = LoginWindow(args.plist)
    window.show()
    return app.exec()
