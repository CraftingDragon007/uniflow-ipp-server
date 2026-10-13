from __future__ import annotations

import json
import logging

from PySide6.QtCore import QFile, QIODevice, QObject, Signal, Slot
from PySide6.QtWebChannel import QWebChannel
from PySide6.QtWebEngineCore import QWebEnginePage, QWebEngineScript, QWebEngineUrlRequestInterceptor

from .protocol import LoginError
from .diagnostics import console_summary, safe_origin


logger = logging.getLogger(__name__)


class RequestLogger(QWebEngineUrlRequestInterceptor):
    def interceptRequest(self, info):
        method = bytes(info.requestMethod()).decode("ascii", errors="replace")
        if method not in ("GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"):
            method = "other"
        logger.debug("Browser request method=%s resource=%s origin=%s", method, info.resourceType().name, safe_origin(info.requestUrl().toString()))


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
            logger.warning("Authentication callback blocked: untrusted page origin=%s", safe_origin(self.page.url().toString()))
            self.notice.emit("Registration callback blocked: it did not originate from the tenant page.")
        elif not message or len(message) > 1 << 20:
            logger.warning("Authentication callback blocked: empty or oversized payload")
            self.notice.emit("Registration callback contains invalid or oversized data.")
        else:
            logger.info("Native authentication callback received; payload contents suppressed")
            self.authentication.emit(message)

    @Slot()
    def registrationFailed(self):
        if self.trusted_page():
            logger.error("Tenant sent the native registration-failed callback")
            self.failure.emit()
        else:
            logger.warning("Registration-failed callback blocked: untrusted page")

    @Slot(str)
    def diagnostic(self, event: str):
        if event in ("shim-ready", "transport-ready", "invalid-callback-type", "callback-queue-full"):
            logger.log(
                logging.WARNING if event in ("invalid-callback-type", "callback-queue-full") else logging.INFO,
                "Browser bridge event=%s trusted_page=%s", event, self.trusted_page(),
            )


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
        if (typeof data !== "string") {
            window.postMessage({uniflowRegistration:true, kind:"diagnostic", data:"invalid-callback-type"}, origin);
            return;
        }
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
    send("diagnostic", "shim-ready");
})();
""".replace("ORIGIN", origin)
        isolated_script = channel_js + """
(() => {
    const origin = ORIGIN;
    if (window !== window.top || location.origin !== origin) return;
    let bridge = null;
    let queueFull = false;
    const pending = [];
    const dispatch = data => {
        if (data.kind === "authentication") bridge.authenticate(data.data);
        else if (data.kind === "failure") bridge.registrationFailed();
        else if (data.kind === "diagnostic") bridge.diagnostic(data.data);
    };
    window.addEventListener("message", event => {
        if (event.source !== window || event.origin !== origin) return;
        const data = event.data;
        if (!data || data.uniflowRegistration !== true ||
            typeof data.data !== "string" || data.data.length > 1048576) return;
        if (bridge) dispatch(data);
        else if (pending.length < 4) pending.push(data);
        else queueFull = true;
    });
    new QWebChannel(qt.webChannelTransport, channel => {
        bridge = channel.objects.registration;
        bridge.diagnostic("transport-ready");
        if (queueFull) bridge.diagnostic("callback-queue-full");
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
        self.urlChanged.connect(lambda url: logger.info("Browser location origin=%s (path/query/fragment suppressed)", safe_origin(url.toString())))
        self.loadingChanged.connect(self._loading_changed)
        self.renderProcessTerminated.connect(lambda status, code: logger.log(
            logging.DEBUG if status == self.RenderProcessTerminationStatus.NormalTerminationStatus else logging.ERROR,
            "Browser renderer terminated status=%s exit_code=%s", status.name, code,
        ))
        self.loadFinished.connect(lambda success: self.log_diagnostics() if success else None)

    def _loading_changed(self, info):
        logger.info(
            "Browser load status=%s origin=%s error_domain=%s error_code=%s",
            info.status().name, safe_origin(info.url().toString()), info.errorDomain().name, info.errorCode(),
        )

    def log_diagnostics(self):
        if not self.bridge.trusted_page():
            logger.debug("Tenant bridge not injected into this non-tenant page")
            return
        self.runJavaScript("""
(() => {
    const handlers = window.webkit && window.webkit.messageHandlers;
    const type = window.PageConfig && window.PageConfig.ClientType;
    return JSON.stringify({
        clientType: ["SmartClientForMac","SmartClientForWindows","LocalServerAgent"].includes(type) ? type : "unknown",
        authenticationHandler: !!(handlers && handlers.smartClientMacAuthenticationData &&
            typeof handlers.smartClientMacAuthenticationData.postMessage === "function"),
        failureHandler: !!(handlers && handlers.smartClientMacRegistrationFailed),
        externalBrowserHandler: !!(handlers && handlers.smartClientMacOpenInSystemBrowser),
        successFunction: typeof SmartClientOnRegistrationSuccess === "function",
        errorFunction: typeof SmartClientOnRegistrationError === "function"
    });
})();
""", self._diagnostics_received)

    def _diagnostics_received(self, result):
        if not isinstance(result, str):
            logger.debug("Tenant page diagnostic probe unavailable during navigation or teardown")
            return
        try:
            result = json.loads(result)
        except ValueError:
            logger.warning("Tenant page diagnostic probe returned invalid data")
            return
        if not isinstance(result, dict):
            logger.warning("Tenant page diagnostic probe returned invalid data")
            return
        client_type = result.get("clientType")
        if client_type not in ("SmartClientForMac", "SmartClientForWindows", "LocalServerAgent"):
            client_type = "unknown"
        flags = {key: result.get(key) is True for key in (
            "authenticationHandler", "failureHandler", "externalBrowserHandler", "successFunction", "errorFunction",
        )}
        logger.info("Tenant page client_type=%s bridge_flags=%s", client_type, flags)

    def _certificate_error(self, error):
        error.rejectCertificate()
        logger.error("Browser TLS certificate rejected")
        self.notice.emit("A TLS certificate was rejected. Fix certificate trust; verification is never bypassed.")

    def acceptNavigationRequest(self, url, navigation_type, is_main_frame):
        logger.debug("Navigation origin=%s main_frame=%s", safe_origin(url.toString()), is_main_frame)
        if url.toString() == "about:blank":
            return True
        if url.scheme() == "https" and url.host() and not url.userName() and not url.password():
            return True
        self.notice.emit("Navigation blocked: login pages must use HTTPS.")
        logger.warning("Non-HTTPS or credential-bearing navigation blocked")
        return False

    def createWindow(self, window_type):
        logger.info("Identity-provider popup requested")
        return self.popup_factory() if self.popup_factory else None

    def chooseFiles(self, mode, old_files, accepted_mime_types):
        self.notice.emit("File uploads are disabled in the login browser.")
        logger.warning("Browser file upload blocked")
        return []

    def javaScriptConsoleMessage(self, level, message, line_number, source_id):
        severity = {
            self.JavaScriptConsoleMessageLevel.InfoMessageLevel: logging.DEBUG,
            self.JavaScriptConsoleMessageLevel.WarningMessageLevel: logging.WARNING,
            self.JavaScriptConsoleMessageLevel.ErrorMessageLevel: logging.ERROR,
        }[level]
        logger.log(severity, "JavaScript %s line=%s origin=%s", console_summary(message), line_number, safe_origin(source_id))
