from __future__ import annotations

import json

from PySide6.QtCore import QFile, QIODevice, QObject, Signal, Slot
from PySide6.QtWebChannel import QWebChannel
from PySide6.QtWebEngineCore import QWebEnginePage, QWebEngineScript

from .protocol import LoginError


class RegistrationBridge(QObject):
    authentication = Signal(str)
    failure = Signal()
    notice = Signal(str)

    def __init__(self, page: QWebEnginePage, tenant: str):
        super().__init__(page)
        self.page = page
        self.tenant = tenant

    def trusted_page(self) -> bool:
        url = self.page.url()
        return url.scheme() == "https" and url.host().lower() == self.tenant and url.port(443) == 443

    @Slot(str)
    def authenticate(self, message: str):
        if not self.trusted_page():
            self.notice.emit("Registration callback blocked: it did not originate from the tenant page.")
        elif not message or len(message) > 1 << 20:
            self.notice.emit("Registration callback contains invalid or oversized data.")
        else:
            self.authentication.emit(message)

    @Slot()
    def registrationFailed(self):
        if self.trusted_page():
            self.failure.emit()


class RegistrationPage(QWebEnginePage):
    notice = Signal(str)

    def __init__(self, profile, tenant: str, parent=None, popup_factory=None):
        super().__init__(profile, parent)
        self.popup_factory = popup_factory
        self.bridge = RegistrationBridge(self, tenant)
        self.bridge.notice.connect(self.notice)
        self.channel = QWebChannel(self)
        self.channel.registerObject("registration", self.bridge)
        self.setWebChannel(self.channel, QWebEngineScript.ScriptWorldId.ApplicationWorld.value)
        source = QFile(":/qtwebchannel/qwebchannel.js")
        if not source.open(QIODevice.OpenModeFlag.ReadOnly):
            raise LoginError("Qt WebChannel JavaScript resource is unavailable. Check the Qt installation.")
        channel_js = bytes(source.readAll()).decode("utf-8")
        source.close()
        origin = json.dumps("https://" + tenant)
        main_script = """
(() => {
    const origin = ORIGIN;
    if (window !== window.top || location.origin !== origin) return;
    const send = (kind, data = "") => {
        if (typeof data !== "string") return;
        window.postMessage({uniflowRegistration: true, kind, data}, origin);
    };
    window.webkit = window.webkit || {};
    window.webkit.messageHandlers = window.webkit.messageHandlers || {};
    window.webkit.messageHandlers.smartClientMacAuthenticationData = {
        postMessage: data => send("authentication", data)
    };
    window.webkit.messageHandlers.smartClientMacRegistrationFailed = {
        postMessage: () => send("failure")
    };
})();
""".replace("ORIGIN", origin)
        isolated_script = channel_js + """
(() => {
    const origin = ORIGIN;
    if (window !== window.top || location.origin !== origin) return;
    let bridge = null;
    const pending = [];
    const dispatch = data => {
        if (data.kind === "authentication") bridge.authenticate(data.data);
        else if (data.kind === "failure") bridge.registrationFailed();
    };
    window.addEventListener("message", event => {
        if (event.source !== window || event.origin !== origin) return;
        const data = event.data;
        if (!data || data.uniflowRegistration !== true ||
            typeof data.data !== "string" || data.data.length > 1048576) return;
        if (bridge) dispatch(data);
        else if (pending.length < 4) pending.push(data);
    });
    new QWebChannel(qt.webChannelTransport, channel => {
        bridge = channel.objects.registration;
        pending.splice(0).forEach(dispatch);
    });
})();
""".replace("ORIGIN", origin)
        for name, script_source, world in (
            ("uniFLOW tenant bridge", main_script, QWebEngineScript.ScriptWorldId.MainWorld),
            ("uniFLOW isolated transport", isolated_script, QWebEngineScript.ScriptWorldId.ApplicationWorld),
        ):
            script = QWebEngineScript()
            script.setName(name)
            script.setInjectionPoint(QWebEngineScript.InjectionPoint.DocumentCreation)
            script.setWorldId(world.value)
            script.setRunsOnSubFrames(False)
            script.setSourceCode(script_source)
            self.scripts().insert(script)
        self.certificateError.connect(self._certificate_error)
        self.permissionRequested.connect(lambda permission: permission.deny())
        self.windowCloseRequested.connect(lambda: self.parent().close() if self.parent() else None)

    def _certificate_error(self, error):
        error.rejectCertificate()
        self.notice.emit("A TLS certificate was rejected. Fix certificate trust; verification is never bypassed.")

    def acceptNavigationRequest(self, url, navigation_type, is_main_frame):
        if url.toString() == "about:blank":
            return True
        if url.scheme() == "https" and url.host() and not url.userName() and not url.password():
            return True
        self.notice.emit("Navigation blocked: login pages must use HTTPS.")
        return False

    def createWindow(self, window_type):
        return self.popup_factory() if self.popup_factory else None

    def chooseFiles(self, mode, old_files, accepted_mime_types):
        self.notice.emit("File uploads are disabled in the login browser.")
        return []

    def javaScriptConsoleMessage(self, level, message, line_number, source_id):
        # Provider console output can contain credentials or callback data.
        pass
