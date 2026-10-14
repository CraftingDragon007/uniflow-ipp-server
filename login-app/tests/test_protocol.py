import base64
import io
import json
import plistlib
import tempfile
import tomllib
import unittest
import uuid
from datetime import datetime, timedelta, timezone
from pathlib import Path
from unittest.mock import patch
from urllib.error import HTTPError
from urllib.parse import parse_qs, quote_plus, unquote_plus, urlsplit

from cryptography.hazmat.primitives import padding
from cryptography.hazmat.primitives.asymmetric import padding as rsa_padding
from cryptography.hazmat.primitives.ciphers import Cipher, algorithms, modes

from uniflow_login.export import export_bundle
from uniflow_login.protocol import (
    Credentials, LoginError, LoginResult, OAuthClient, RegistrationSession, Tenant,
    _short_secret, hostname,
)


def tenant():
    return Tenant("tenant.example.invalid", "test-installer", 'Test "installer"', "2026.3.0", "TEST_QUEUE", True, True)


def credentials():
    return Credentials(
        "test-client", "test-secret+&", "test-scope offline_access",
        "https://oauth.example.invalid/token", "https://tenant.example.invalid/callback",
        "test-code", "12345678-90ab-cdef-1234-567890abcdef",
        datetime.now(timezone.utc) + timedelta(days=30),
    )


def authorization_message(session, changes=None, plaintext=None):
    creds = credentials()
    aes_key = bytes(range(32))
    ticks = int((creds.expires_at - datetime(1601, 1, 1, tzinfo=timezone.utc)).total_seconds()) * 10_000_000
    if plaintext is None:
        plaintext = "&".join(quote_plus(value) for value in (str(ticks), creds.client_id, creds.client_secret, creds.scope))
    padder = padding.PKCS7(128).padder()
    padded = padder.update(plaintext.encode("utf-8")) + padder.finalize()
    encryptor = Cipher(algorithms.AES(aes_key), modes.CBC(bytes(16))).encryptor()
    response = {
        "Key": base64.b64encode(session.key.public_key().encrypt(aes_key, rsa_padding.PKCS1v15())).decode(),
        "Data": base64.b64encode(encryptor.update(padded) + encryptor.finalize()).decode(),
        "TokenUri": creds.token_uri, "RedirectUri": creds.redirect_uri,
        "Code": creds.code, "UserId": creds.user_id,
    }
    response.update(changes or {})
    return base64.b64encode(json.dumps(response).encode()).decode()


class Response(io.BytesIO):
    status = 200


def result():
    return LoginResult(credentials(), {
        "access_token": "test-access", "refresh_token": "test-refresh",
        "token_type": "Bearer", "expires_in": 3600,
        "expires_at": (datetime.now(timezone.utc) + timedelta(hours=1)).isoformat().replace("+00:00", "Z"),
        "scope": "test-scope offline_access",
    })


class ProtocolTests(unittest.TestCase):
    def test_plist_xml_and_binary(self):
        for fmt in (plistlib.FMT_XML, plistlib.FMT_BINARY):
            with self.subTest(fmt=fmt), tempfile.TemporaryDirectory() as temp:
                path = Path(temp) / ".tenantcfg.plist"
                path.write_bytes(plistlib.dumps({
                    "UFOTenantDomain": "tenant.example.invalid",
                    "InstallerName": "Test installer",
                    "IgnoreSystemLoginInformation": "true",
                    "UsePublicPrintMode": False,
                    "SupportFinishingOptions": True,
                }, fmt=fmt))
                parsed = Tenant.load(path)
                self.assertEqual(parsed.domain, "tenant.example.invalid")
                self.assertTrue(parsed.ignore_system_login)
                self.assertTrue(parsed.support_finishings)

    def test_invalid_plists_and_hosts(self):
        with tempfile.TemporaryDirectory() as temp:
            path = Path(temp) / "bad.plist"
            for data in (
                b"<plist><dict>broken",
                plistlib.dumps({"UFOTenantDomain": "https://tenant.example.invalid"}),
                plistlib.dumps({"UFOTenantDomain": "tenant.example.invalid", "UsePublicPrintMode": "true"}),
                plistlib.dumps({"UFOTenantDomain": "tenant.example.invalid", "SupportFinishingOptions": "maybe"}),
                plistlib.dumps(["not", "a", "dictionary"]),
            ):
                path.write_bytes(data)
                with self.subTest(data=data), self.assertRaises(LoginError):
                    Tenant.load(path)
        for host in ("localhost", "127.0.0.1", "[::1]", "user@example.invalid", "example.invalid/path", "x:443", ""):
            with self.subTest(host=host), self.assertRaises(LoginError):
                hostname(host)

    def test_registration_url_matches_native_fields(self):
        import hashlib
        session = RegistrationSession(tenant())
        now = datetime(2026, 10, 13, tzinfo=timezone.utc)
        with patch("getpass.getuser", return_value="test-user"), patch("socket.gethostname", return_value="test-host"), patch("socket.getfqdn", return_value="test-host.example.invalid"):
            parsed = urlsplit(session.registration_url(now))
        self.assertEqual(parsed.path, "/Home/SmartClientRegistration")
        self.assertEqual(parse_qs(parsed.query)["Username"], ["test-user"])
        fields = dict(part.split("=", 1) for part in unquote_plus(parsed.fragment).split("|"))
        expected = hashlib.md5(("13102026" + session.exponent + session.modulus + session.secret).encode(), usedforsecurity=False).hexdigest()
        self.assertEqual(fields["HMAC"], expected)
        self.assertEqual(fields["Exponent"], "AQAB")
        self.assertEqual(len(base64.b64decode(fields["Modulus"])), 256)
        self.assertEqual(fields["IgnoreSystemLoginInformation"], "true")
        self.assertEqual(fields["ManualValidation"], "false")
        self.assertEqual(fields["UsePublicPrintMode"], "false")
        self.assertEqual(_short_secret(uuid.UUID(int=0)), "00")

    def test_decrypt_and_reject_replay(self):
        session = RegistrationSession(tenant())
        message = authorization_message(session)
        got = session.decode_authorization(message)
        expected = credentials()
        self.assertEqual(got.client_secret, expected.client_secret)
        self.assertEqual(got.scope, expected.scope)
        self.assertEqual(got.user_id, expected.user_id)
        self.assertGreater(got.expires_at, datetime.now(timezone.utc))
        with self.assertRaises(LoginError):
            RegistrationSession(tenant()).decode_authorization(message)
        self.assertNotIn("test-secret", repr(got))

    def test_invalid_registration_fields(self):
        session = RegistrationSession(tenant())
        for changes in (
            {"UserId": str(uuid.UUID(int=0))}, {"UserId": "not-a-uuid"},
            {"TokenUri": "http://oauth.example.invalid/token"},
            {"TokenUri": "https://oauth.example.invalid/token#unsupported"},
            {"TokenUri": "https://localhost/token"}, {"RedirectUri": "relative"},
            {"RedirectUri": "https://["}, {"Code": ""},
            {"Key": "not base64"}, {"Data": "not base64"},
        ):
            with self.subTest(changes=changes), self.assertRaises(LoginError):
                session.decode_authorization(authorization_message(session, changes))
        for plaintext in ("0&client&secret&scope", "only-one-field"):
            with self.assertRaises(LoginError):
                session.decode_authorization(authorization_message(session, plaintext=plaintext))
        for message in ("not-base64", base64.b64encode(b"[]").decode(), base64.b64encode(b"{}").decode()):
            with self.assertRaises(LoginError):
                session.decode_authorization(message)

    def test_oauth_grant_and_token_export(self):
        client = OAuthClient()
        response = {"access_token": "test-access", "refresh_token": "test-refresh", "token_type": "Bearer", "expires_in": "3600"}
        with patch.object(client.opener, "open", return_value=Response(json.dumps(response).encode())) as opened:
            got = client.exchange(credentials())
        request = opened.call_args.args[0]
        form = parse_qs(request.data.decode())
        self.assertEqual(form["grant_type"], ["authorization_code"])
        self.assertEqual(form["client_secret"], ["test-secret+&"])
        self.assertEqual(form["redirect_uri"], [credentials().redirect_uri])
        self.assertEqual(request.get_header("X-clienttype"), "SmartClientForMac")
        self.assertEqual(got.token["token_type"], "Bearer")
        self.assertEqual(got.token["expires_in"], 3600)
        self.assertEqual(got.credentials.code, "")
        self.assertNotIn("test-access", repr(got))

    def test_oauth_errors_are_private(self):
        client = OAuthClient()
        for response in (
            {},
            {"access_token": "test", "token_type": "Bearer", "expires_in": -1},
            {"access_token": "test\nprivate", "token_type": "Bearer", "expires_in": 3600},
            {"access_token": "test", "token_type": "Bearer", "expires_in": True},
            {"access_token": "test", "token_type": 42, "expires_in": 3600},
            {"access_token": "test", "token_type": {}, "expires_in": 3600},
        ):
            with patch.object(client.opener, "open", return_value=Response(json.dumps(response).encode())):
                with self.assertRaises(LoginError):
                    client.exchange(credentials())
        error = HTTPError("https://private.example.invalid/code-secret", 400, "test-private-message", {}, Response(b"test-private-response"))
        with self.assertLogs("uniflow_login", level="INFO") as captured, patch.object(client.opener, "open", side_effect=error):
            with self.assertRaises(LoginError) as caught:
                client.exchange(credentials())
        self.assertEqual(str(caught.exception), "OAuth token endpoint returned HTTP 400; restart login.")
        output = "\n".join(captured.output)
        for secret in ("code-secret", "test-private-message", "test-private-response", "test-secret+&"):
            self.assertNotIn(secret, output)

    def test_native_token_response_with_implicit_bearer(self):
        client = OAuthClient()
        for fields in (
            {}, {"token_type": ""}, {"token_type": None}, {"token_type": "bEaReR"},
            {"token_type": "JWT"}, {"token_type": "Basic"}, {"token_type": "private-label"},
        ):
            with self.subTest(fields=fields):
                response = {"access_token": "test-native-token", "expires_in": 3600, **fields}
                with self.assertLogs("uniflow_login", level="INFO") as captured, patch.object(
                    client.opener, "open", return_value=Response(json.dumps(response).encode()),
                ):
                    got = client.exchange(credentials())
                self.assertEqual(got.token["token_type"], "Bearer")
                self.assertEqual(got.token["access_token"], "test-native-token")
                self.assertNotIn("test-native-token", "\n".join(captured.output))
                self.assertNotIn("private-label", "\n".join(captured.output))
                if not fields.get("token_type"):
                    self.assertIn("implicit Bearer contract", "\n".join(captured.output))
                elif fields["token_type"] != "bEaReR":
                    self.assertIn("fixed Bearer authorization scheme", "\n".join(captured.output))

    def test_private_atomic_export_and_service_paths(self):
        with tempfile.TemporaryDirectory() as temp:
            parent = Path(temp)
            for system in (False, True):
                directory = export_bundle(tenant(), result(), parent, tenant().domain, system)
                self.assertEqual(directory.stat().st_mode & 0o777, 0o700)
                for name in ("config.toml", "token.json"):
                    self.assertEqual((directory / name).stat().st_mode & 0o777, 0o600)
                cfg = tomllib.loads((directory / "config.toml").read_text())
                token = json.loads((directory / "token.json").read_text())
                self.assertTrue(cfg["cloud"]["enabled"])
                self.assertEqual(cfg["cloud"]["oauth_client_secret"], credentials().client_secret)
                self.assertEqual(cfg["tenant"]["installer_name"], tenant().installer_name)
                self.assertEqual(cfg["cloud"]["management_server"], tenant().domain)
                self.assertEqual(token["access_token"], "test-access")
                expected_path = "/var/lib/uniflow-ipp/token.json" if system else str(directory / "token.json")
                self.assertEqual(cfg["cloud"]["token_path"], expected_path)
                self.assertEqual((directory / "spool").exists(), not system)
            self.assertEqual(len(list(parent.iterdir())), 2)

    def test_managed_export_includes_renewal_key(self):
        with tempfile.TemporaryDirectory() as temp:
            session = RegistrationSession(tenant())
            directory = export_bundle(tenant(), result(), Path(temp), tenant().domain, True, session.key)
            config = tomllib.loads((directory / "config.toml").read_text())
            state = json.loads((directory / "auth-state.json").read_text())
            self.assertEqual(config["cloud"]["auth_state_path"], "/var/lib/uniflow-ipp/auth-state.json")
            self.assertNotIn("oauth_client_secret", config["cloud"])
            self.assertEqual(state["version"], 2)
            self.assertEqual(state["peer"]["client_secret"], credentials().client_secret)
            self.assertEqual(state["token"]["refresh_token"], "test-refresh")
            self.assertIn("BEGIN PRIVATE KEY", state["renewal_private_key_pem"])
            self.assertEqual((directory / "auth-state.json").stat().st_mode & 0o777, 0o600)
            self.assertFalse((directory / "token.json").exists())

    def test_export_failure_leaves_no_partial_bundle(self):
        with tempfile.TemporaryDirectory() as temp:
            with patch("os.rename", side_effect=OSError("test write failure")), self.assertRaises(LoginError):
                export_bundle(tenant(), result(), Path(temp), tenant().domain)
            self.assertEqual(list(Path(temp).iterdir()), [])
        expired = result()
        expired.token["expires_at"] = "2000-01-01T00:00:00Z"
        with self.assertRaises(LoginError):
            export_bundle(tenant(), expired, Path("."), tenant().domain)


if __name__ == "__main__":
    unittest.main()
