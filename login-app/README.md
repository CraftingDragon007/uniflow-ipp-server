# uniFLOW Login

A separate PySide6/Qt WebEngine desktop application for logging in to the
tenant specified by a SmartClient `.tenantcfg.plist` and generating the Linux
IPP server's configuration. It does not run the server, create a CUPS queue,
or change system configuration automatically.

## Run

From the server repository:

```sh
uv run --project login-app uniflow-login --plist /path/to/.tenantcfg.plist
```

Alternatively, install this project into a Python virtual environment with
`pip install ./login-app`, then run `uniflow-login`. Python 3.11 or newer is
required. `uv.lock` records the tested dependencies.

The plist can be XML or binary, with native booleans or the string booleans
used by the macOS installer. Open the plist, click **Log in**, and complete the
tenant's login/MFA in the embedded browser. The app receives tenant-issued
peer credentials and exchanges the authorization code for OAuth tokens.
OAuth endpoints, scope and client credentials come from that response, not
from guessed Microsoft device-flow defaults.

After login, choose **Export server configuration**. The app creates a new
private directory containing `config.toml` and `token.json`. Exports do not
overwrite an existing configuration. In the default **current-user mode**,
the config points to the exported token and a private spool directory; run
the command displayed in the app to start the server.

## Packaged system service

Select **Export paths for the packaged system service** before exporting.
The app then generates paths matching the Arch server package:

- token: `/var/lib/uniflow-ipp/token.json`
- spool: `/var/spool/uniflow-ipp`
- CUPS queue: `uniflow_secure`

Install the server package first. The app displays commands to install the
configuration as `root:uniflow-ipp` mode `0640`, install the token as
`uniflow-ipp:uniflow-ipp` mode `0600`, and restart the service. They must be
run manually; the GUI itself never needs root.

The management-server field defaults to `UFOTenantDomain`, matching the
macOS client's actual initialization. Change it only if the tenant requires
a different management hostname. Installer name/version/ID and secure queue
name are carried into the generated configuration. Authentication respects
`IgnoreSystemLoginInformation`; public-print plists are rejected because the
server implements secure PDF submission, not the public-print workflow.
`UseIPP` and `SupportFinishingOptions` are installer printer-setup flags;
this login app does not install queues or change the server's PPD capabilities.

## Arch package

This is independently packaged from the server:

```sh
# From the server repository, after committing your source changes:
bash scripts/make-source.sh
cp uniflow-ipp-server-1.0.0.tar.gz login-app/
cd login-app
makepkg -si
uniflow-login --plist /path/to/.tenantcfg.plist
```

The package adds a **uniFLOW Login** desktop entry. Arch's `pyside6` and Qt
WebEngine packages must be ABI-compatible. If system bindings fail with an
undefined-symbol import error, use the isolated `uv run` command above rather
than mixing system Qt libraries with pip-installed bindings. The source
runtime and browser tests use matching PySide6/Qt wheels.

## Diagnostics

Application logs appear in the terminal and in the read-only panel below the
browser by default. To capture a more detailed run from the server repository:

```sh
uv run --project login-app uniflow-login --plist /path/to/.tenantcfg.plist \
  --debug --log-file ~/uniflow-login.log
```

The optional log file is appended to, made mode `0600`, and cannot be a symlink.
Logs show navigation/load error codes, renderer failures, popup creation,
bridge initialization, callback receipt/validation, OAuth HTTP status and TLS
failure categories. `--debug` adds request-origin/resource-type events.
**Log browser diagnostics** probes the current tenant page's native client
type and callback availability. A waiting-state snapshot is also logged every
30 seconds until a callback arrives.

URL paths, query strings, fragments, request/response bodies, authorization
codes, tokens and client secrets are never logged. Provider JavaScript console
text is not copied verbatim: only fixed error categories and known bridge
identifiers are retained. Uncaught Python errors report their class and
function/line stack, not potentially secret exception text. Logs still include
tenant/identity-provider hostnames; review them before sharing.

The macOS identity adjustment addresses a confirmed contract mismatch, but
the live post-MFA spinner was still reported afterward. The diagnostics are
intended to distinguish a missing callback/bridge failure from a rejected
registration response or failed token exchange; live completion is not yet
established.

## Privacy and compatibility

The browser profile is off-the-record: cookies and cache stay in memory.
Every login uses a newly generated RSA key. The JavaScript compatibility
bridge is injected only into the tenant's main frame. Its native transport
lives in an isolated Qt script world, with source/origin checks; federated
identity-provider pages and their frames cannot submit registration data.
The profile advertises a macOS platform to select the tenant's
`SmartClientForMac` callback contract, retaining the actual Qt/Chromium browser
versions. With the default Linux identity the tenant selects Windows callbacks,
which can leave the registration spinner running after successful login/MFA.

The implementation follows the shipped protocol's MD5-named `HMAC` field,
RSA PKCS#1 v1.5 key wrapping and AES-CBC/PKCS#7 response. These choices are
compatibility requirements, not newly designed cryptography. TLS validation
and the Chromium sandbox remain enabled. OAuth POST redirects are not
followed. Private keys and browser credentials are not exported or logged.
Token and config files contain secrets and are written as mode `0600` in a
mode `0700` directory. Do not commit or share them.

Initial login/configuration export is implemented; live tenant acceptance
has not been established by offline tests. Embedded-browser policies at an
identity provider may refuse login. The server's regular access-token refresh
is supported, but peer credential renewal and continuous peer-registration
heartbeats are not. The config records credential expiration in a comment;
log in again when those credentials need renewing. A cloud upload still
requires the tenant to accept this unofficial client.

## Tests

```sh
uv run --project login-app python -m unittest discover -s login-app/tests -p test_protocol.py
uv run --project login-app python -m unittest discover -s login-app/tests -p test_https.py
QT_QPA_PLATFORM=xcb QTWEBENGINE_CHROMIUM_FLAGS=--disable-gpu xvfb-run -a \
  uv run --project login-app python -m unittest discover -s login-app/tests -p test_browser.py
```

These use synthetic encrypted responses, a local verified-TLS OAuth fixture,
and actual Qt WebEngine/WebChannel pages. The GUI test covers browser callback
through authorization-code exchange and private config export. No live
tenant credentials or documents are used.
