from __future__ import annotations

import base64
import binascii
import getpass
import hashlib
import ipaddress
import json
import logging
import plistlib
import re
import socket
import ssl
import uuid
from dataclasses import dataclass, field, replace
from datetime import datetime, timedelta, timezone
from pathlib import Path
from http.client import HTTPException
from urllib.error import HTTPError, URLError
from urllib.parse import unquote_plus, urlencode, urlsplit
from urllib.request import HTTPRedirectHandler, HTTPSHandler, Request, build_opener
from xml.parsers.expat import ExpatError

from cryptography.hazmat.primitives import padding
from cryptography.hazmat.primitives.asymmetric import padding as rsa_padding
from cryptography.hazmat.primitives.asymmetric import rsa
from cryptography.hazmat.primitives.ciphers import Cipher, algorithms, modes

from .diagnostics import safe_origin


logger = logging.getLogger(__name__)


class LoginError(Exception):
    """An actionable error whose message contains no authentication material."""


def hostname(value: str) -> str:
    if not isinstance(value, str) or not value or len(value) > 253:
        raise LoginError("A valid tenant or management-server hostname is required.")
    try:
        host = value.encode("idna").decode("ascii").lower()
    except UnicodeError as exc:
        raise LoginError("Invalid hostname encoding.") from exc
    if host == "localhost" or host.endswith(".localhost"):
        raise LoginError("Localhost cannot be used as a tenant or management server.")
    try:
        ipaddress.ip_address(host)
    except ValueError:
        pass
    else:
        raise LoginError("Use a DNS hostname, not an IP address.")
    if any(not re.fullmatch(r"[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?", label) for label in host.split(".")):
        raise LoginError("Use a hostname without a scheme, port, path or credentials.")
    return host


def https_endpoint(value: str, *, allow_fragment: bool = False) -> str:
    if not isinstance(value, str) or len(value) > 8192 or any(ch.isspace() for ch in value):
        raise LoginError("The tenant returned an invalid HTTPS endpoint.")
    try:
        parsed = urlsplit(value)
        port = parsed.port
    except ValueError as exc:
        raise LoginError("The tenant returned an invalid HTTPS endpoint.") from exc
    if parsed.scheme != "https" or not parsed.hostname or parsed.username is not None or (parsed.fragment and not allow_fragment):
        raise LoginError("A valid HTTPS URL without embedded credentials or unsupported fragments is required.")
    hostname(parsed.hostname)
    if port is not None and not 1 <= port <= 65535:
        raise LoginError("The tenant returned an invalid HTTPS port.")
    return value


def _plist_bool(values: dict, name: str, default: bool) -> bool:
    value = values.get(name, default)
    if isinstance(value, bool):
        return value
    if isinstance(value, str) and value.lower() in ("true", "false"):
        return value.lower() == "true"
    raise LoginError(f"Plist field {name} must be a boolean or 'true'/'false'.")


def _plist_text(values: dict, name: str, default: str = "") -> str:
    value = values.get(name, default)
    if not isinstance(value, str) or len(value) > 4096 or "\0" in value:
        raise LoginError(f"Plist field {name} must be text without null characters.")
    return value


@dataclass(frozen=True)
class Tenant:
    domain: str
    installer_id: str
    installer_name: str
    product_version: str
    secure_queue: str
    ignore_system_login: bool
    support_finishings: bool

    @classmethod
    def load(cls, path: Path) -> Tenant:
        try:
            if path.stat().st_size > 1 << 20:
                raise LoginError("Tenant plist exceeds the 1 MiB size limit.")
            with path.open("rb") as stream:
                values = plistlib.load(stream)
        except (OSError, ValueError, plistlib.InvalidFileException, ExpatError) as exc:
            raise LoginError("Cannot read the tenant plist; select a valid XML or binary plist.") from exc
        if not isinstance(values, dict):
            raise LoginError("Tenant plist must contain a dictionary.")
        if _plist_bool(values, "UsePublicPrintMode", False):
            raise LoginError("Public print mode is not supported by this secure-print server.")
        return cls(
            domain=hostname(_plist_text(values, "UFOTenantDomain")),
            installer_id=_plist_text(values, "InstallerId"),
            installer_name=_plist_text(values, "InstallerName", "Linux IPP Client"),
            product_version=_plist_text(values, "ProductVersion", "1.0.0"),
            secure_queue=_plist_text(values, "SecurePrintQueueName", "uniflow_secure"),
            ignore_system_login=_plist_bool(values, "IgnoreSystemLoginInformation", False),
            support_finishings=_plist_bool(values, "SupportFinishingOptions", True),
        )


def _short_secret(value: uuid.UUID) -> str:
    # Match the shipped helper: it emits base-36 digits least-significant first.
    def digits(number: int) -> str:
        result = ""
        while True:
            number, remainder = divmod(number, 36)
            result += "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ"[remainder]
            if number == 0:
                return result

    high = (value.time_low << 32) | (value.time_mid << 16) | value.time_hi_version
    low = int.from_bytes(value.bytes_le[8:], "little")
    return digits(high) + digits(low)


def _b64(data: bytes) -> str:
    return base64.b64encode(data).decode("ascii")


def _decode64(value: str) -> bytes:
    if not isinstance(value, str) or len(value) > 1 << 20:
        raise LoginError("Invalid registration data.")
    try:
        return base64.b64decode("".join(value.split()), validate=True)
    except (ValueError, binascii.Error) as exc:
        raise LoginError("Registration data is not valid Base64.") from exc


@dataclass(frozen=True, repr=False)
class Credentials:
    client_id: str
    client_secret: str
    scope: str
    token_uri: str
    redirect_uri: str
    code: str
    user_id: str
    expires_at: datetime


class RegistrationSession:
    def __init__(self, tenant: Tenant):
        self.tenant = tenant
        self.key = rsa.generate_private_key(public_exponent=65537, key_size=2048)
        public = self.key.public_key().public_numbers()
        self.modulus = _b64(public.n.to_bytes(256, "big"))
        self.exponent = _b64(public.e.to_bytes(3, "big"))
        self.secret = _short_secret(uuid.uuid4())

    def registration_url(self, now: datetime | None = None) -> str:
        now = now or datetime.now(timezone.utc)
        digest_input = now.strftime("%d%m%Y") + self.exponent + self.modulus + self.secret
        # "HMAC" is the protocol's field name; the shipped client uses plain MD5.
        digest = hashlib.md5(digest_input.encode("ascii"), usedforsecurity=False).hexdigest()
        fqdn = socket.getfqdn()
        user = getpass.getuser()
        fields = {
            "HMAC": digest,
            "Modulus": self.modulus,
            "Exponent": self.exponent,
            "Username": user,
            "Domain": fqdn.partition(".")[2],
            "Host": socket.gethostname(),
            "FQDN": fqdn,
            "ManualValidation": "false",
            "IgnoreSystemLoginInformation": str(self.tenant.ignore_system_login).lower(),
            "UsePublicPrintMode": "false",
        }
        query = urlencode({"Username": user, "UsePublicPrintMode": "false"})
        fragment = urlencode({"": "|".join(f"{key}={value}" for key, value in fields.items())})[1:]
        return f"https://{self.tenant.domain}/Home/SmartClientRegistration?{query}#{fragment}"

    def decode_authorization(self, message: str) -> Credentials:
        try:
            response = json.loads(_decode64(message))
        except (ValueError, UnicodeError) as exc:
            raise LoginError("Registration response is not valid JSON.") from exc
        if not isinstance(response, dict):
            raise LoginError("Registration response must be an object.")
        required = ("Key", "Data", "TokenUri", "RedirectUri", "Code", "UserId")
        if any(not isinstance(response.get(name), str) or not response[name] for name in required):
            raise LoginError("Registration response is missing required credential fields.")
        try:
            user_id = uuid.UUID(response["UserId"])
        except ValueError as exc:
            raise LoginError("The tenant returned an invalid user UUID.") from exc
        if user_id.int == 0:
            raise LoginError("The tenant returned an empty user UUID.")
        token_uri = https_endpoint(response["TokenUri"])
        redirect_uri = response["RedirectUri"]
        try:
            valid_redirect = bool(urlsplit(redirect_uri).scheme) and not any(ch.isspace() for ch in redirect_uri)
        except ValueError as exc:
            raise LoginError("The tenant returned an invalid OAuth redirect URI.") from exc
        if not valid_redirect:
            raise LoginError("The tenant returned an invalid OAuth redirect URI.")
        try:
            aes_key = self.key.decrypt(_decode64(response["Key"]), rsa_padding.PKCS1v15())
            decryptor = Cipher(algorithms.AES(aes_key), modes.CBC(bytes(16))).decryptor()
            padded = decryptor.update(_decode64(response["Data"])) + decryptor.finalize()
            unpadder = padding.PKCS7(128).unpadder()
            plaintext = unpadder.update(padded) + unpadder.finalize()
            parts = plaintext.decode("utf-8-sig").rstrip("\0").split("&")
            if len(parts) < 4:
                raise LoginError("Decrypted registration data is incomplete.")
            expiry, client_id, client_secret, scope = (unquote_plus(item) for item in parts[:4])
            expires_at = datetime(1601, 1, 1, tzinfo=timezone.utc) + timedelta(microseconds=int(expiry) // 10)
        except (ValueError, OverflowError, UnicodeError) as exc:
            raise LoginError("Cannot decrypt registration data for this login session.") from exc
        if not client_id or not client_secret or not scope or expires_at <= datetime.now(timezone.utc):
            raise LoginError("The tenant returned empty or expired OAuth credentials.")
        return Credentials(client_id, client_secret, scope, token_uri, redirect_uri, response["Code"], str(user_id), expires_at)


class _NoRedirect(HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


@dataclass(frozen=True, repr=False)
class LoginResult:
    credentials: Credentials = field(repr=False)
    token: dict = field(repr=False)


class OAuthClient:
    def __init__(self, context: ssl.SSLContext | None = None):
        self.opener = build_opener(_NoRedirect(), HTTPSHandler(context=context or ssl.create_default_context()))

    def exchange(self, credentials: Credentials) -> LoginResult:
        logger.info("OAuth authorization-code exchange started; endpoint_origin=%s", safe_origin(credentials.token_uri))
        form = {
            "grant_type": "authorization_code",
            "client_id": credentials.client_id,
            "client_secret": credentials.client_secret,
            "scope": credentials.scope,
            "redirect_uri": credentials.redirect_uri,
            "code": credentials.code,
        }
        request = Request(
            https_endpoint(credentials.token_uri),
            data=urlencode(form).encode("utf-8"),
            headers={"Content-Type": "application/x-www-form-urlencoded", "X-ClientType": "SmartClientForMac"},
            method="POST",
        )
        try:
            with self.opener.open(request, timeout=30) as response:
                logger.info("OAuth token endpoint returned HTTP %s", response.status)
                if response.status != 200:
                    raise LoginError(f"OAuth token endpoint returned HTTP {response.status}.")
                body = response.read((1 << 20) + 1)
        except HTTPError as exc:
            # Error descriptions and URLs can contain authorization material.
            status = exc.code
            logger.error("OAuth token request rejected: HTTP %s (response body suppressed)", status)
            exc.close()
            raise LoginError(f"OAuth token endpoint returned HTTP {status}; restart login.") from None
        except (URLError, OSError, HTTPException) as exc:
            logger.error("OAuth connection failed: exception_type=%s reason_type=%s", type(exc).__name__, type(getattr(exc, "reason", exc)).__name__)
            raise LoginError("OAuth token request failed. Check connectivity and trusted TLS certificates.") from exc
        if len(body) > 1 << 20:
            raise LoginError("OAuth token response exceeds the 1 MiB size limit.")
        try:
            token = json.loads(body)
        except (ValueError, UnicodeError) as exc:
            raise LoginError("OAuth token endpoint returned invalid JSON.") from exc
        if not isinstance(token, dict) or not isinstance(token.get("access_token"), str) or not token["access_token"]:
            raise LoginError("OAuth token response has no access token.")
        if any(not 33 <= ord(ch) <= 126 for ch in token["access_token"]):
            raise LoginError("OAuth access token contains invalid header characters.")
        token_type = token.get("token_type")
        if token_type is None or token_type == "":
            state = "missing" if "token_type" not in token else ("null" if token_type is None else "empty")
            logger.warning("OAuth token_type is %s; using the native SmartClient's implicit Bearer contract", state)
        elif not isinstance(token_type, str) or token_type.lower() != "bearer":
            state = "unsupported-string" if isinstance(token_type, str) else "invalid-type"
            logger.error("OAuth token_type rejected: state=%s (value suppressed)", state)
            raise LoginError("OAuth token response specifies an unsupported token_type; expected Bearer.")
        lifetime = token.get("expires_in")
        if isinstance(lifetime, str) and lifetime.isdecimal():
            lifetime = int(lifetime)
        if type(lifetime) is not int or not 0 < lifetime <= 31_536_000:
            raise LoginError("OAuth token response has an invalid expires_in.")
        if not isinstance(token.get("refresh_token", ""), str):
            raise LoginError("OAuth refresh token has an invalid type.")
        expires_at = datetime.now(timezone.utc) + timedelta(seconds=lifetime)
        exported_token = {
            "access_token": token["access_token"],
            "refresh_token": token.get("refresh_token", ""),
            "token_type": "Bearer",
            "expires_in": lifetime,
            "expires_at": expires_at.isoformat().replace("+00:00", "Z"),
            "scope": credentials.scope,
        }
        logger.info("OAuth token response validated successfully; token contents suppressed")
        return LoginResult(replace(credentials, code=""), exported_token)
