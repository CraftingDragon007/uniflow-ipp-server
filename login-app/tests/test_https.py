import json
import os
import socket
import ssl
import tempfile
import threading
import unittest
from dataclasses import replace
from datetime import datetime, timedelta, timezone
from http.server import BaseHTTPRequestHandler, HTTPServer
from pathlib import Path
from unittest.mock import patch
from urllib.parse import parse_qs

from cryptography import x509
from cryptography.hazmat.primitives import hashes, serialization
from cryptography.hazmat.primitives.asymmetric import rsa
from cryptography.x509.oid import NameOID

from uniflow_login.protocol import LoginError, OAuthClient
from test_protocol import credentials


class HttpsTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        path = Path(self.temp.name)
        key = rsa.generate_private_key(public_exponent=65537, key_size=2048)
        name = x509.Name([x509.NameAttribute(NameOID.COMMON_NAME, "oauth.example.invalid")])
        now = datetime.now(timezone.utc)
        cert = (
            x509.CertificateBuilder()
            .subject_name(name).issuer_name(name).public_key(key.public_key())
            .serial_number(x509.random_serial_number())
            .not_valid_before(now - timedelta(minutes=1))
            .not_valid_after(now + timedelta(days=1))
            .add_extension(x509.SubjectAlternativeName([x509.DNSName("oauth.example.invalid")]), critical=False)
            .add_extension(x509.BasicConstraints(ca=True, path_length=0), critical=True)
            .sign(key, hashes.SHA256())
        )
        cert_path, key_path = path / "fixture.pem", path / "fixture-key.pem"
        cert_path.write_bytes(cert.public_bytes(serialization.Encoding.PEM))
        key_path.write_bytes(key.private_bytes(serialization.Encoding.PEM, serialization.PrivateFormat.PKCS8, serialization.NoEncryption()))
        self.context = ssl.create_default_context(cafile=str(cert_path))
        self.requests = []
        self.status = 200
        self.token = {"access_token": "test-https", "token_type": "Bearer", "expires_in": 3600}
        owner = self

        class Handler(BaseHTTPRequestHandler):
            def do_POST(self):
                body = self.rfile.read(int(self.headers["Content-Length"]))
                owner.requests.append((self.path, dict(self.headers), parse_qs(body.decode())))
                data = json.dumps(owner.token).encode()
                self.send_response(owner.status)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(data)))
                self.send_header("Location", "https://untrusted.example.invalid/forward")
                self.end_headers()
                self.wfile.write(data)

            def log_message(self, format, *args):
                pass

        self.server = HTTPServer(("127.0.0.1", 0), Handler)
        tls = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
        tls.load_cert_chain(cert_path, key_path)
        self.server.socket = tls.wrap_socket(self.server.socket, server_side=True)
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()
        self.addCleanup(self.stop_server)
        self.endpoint = f"https://oauth.example.invalid:{self.server.server_port}/token"
        self.original_getaddrinfo = socket.getaddrinfo
        self.dns_patch = patch("socket.getaddrinfo", side_effect=self.resolve)
        self.dns_patch.start()
        self.addCleanup(self.dns_patch.stop)
        self.proxy_patch = patch.dict(os.environ, {"NO_PROXY": "oauth.example.invalid", "no_proxy": "oauth.example.invalid"})
        self.proxy_patch.start()
        self.addCleanup(self.proxy_patch.stop)

    def resolve(self, host, port, *args, **kwargs):
        if host != "oauth.example.invalid":
            raise AssertionError("Test attempted a connection outside its HTTPS fixture.")
        return self.original_getaddrinfo("127.0.0.1", port, *args, **kwargs)

    def stop_server(self):
        self.server.shutdown()
        self.thread.join(timeout=5)
        self.server.server_close()

    def test_verified_https_grant(self):
        got = OAuthClient(self.context).exchange(replace(credentials(), token_uri=self.endpoint))
        self.assertEqual(got.token["access_token"], "test-https")
        self.assertEqual(len(self.requests), 1)
        _, headers, form = self.requests[0]
        self.assertEqual(headers["X-Clienttype"], "SmartClientForMac")
        self.assertEqual(form["client_secret"], ["test-secret+&"])
        self.assertEqual(form["grant_type"], ["authorization_code"])

    def test_untrusted_certificate_is_rejected(self):
        with self.assertRaises(LoginError):
            OAuthClient().exchange(replace(credentials(), token_uri=self.endpoint))
        self.assertEqual(self.requests, [])

    def test_native_response_without_token_type(self):
        del self.token["token_type"]
        got = OAuthClient(self.context).exchange(replace(credentials(), token_uri=self.endpoint))
        self.assertEqual(got.token["token_type"], "Bearer")
        self.assertEqual(got.token["access_token"], "test-https")

    def test_native_response_with_non_bearer_label(self):
        self.token["token_type"] = "JWT"
        got = OAuthClient(self.context).exchange(replace(credentials(), token_uri=self.endpoint))
        self.assertEqual(got.token["token_type"], "Bearer")
        self.assertEqual(got.token["access_token"], "test-https")

    def test_redirect_does_not_forward_credentials(self):
        self.status = 302
        with self.assertRaisesRegex(LoginError, "HTTP 302"):
            OAuthClient(self.context).exchange(replace(credentials(), token_uri=self.endpoint))
        self.assertEqual(len(self.requests), 1)


if __name__ == "__main__":
    unittest.main()
