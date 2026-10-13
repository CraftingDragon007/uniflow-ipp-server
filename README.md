# uniFLOW IPP Server for Linux

An experimental local IPP endpoint for CUPS, targeting Arch Linux. This is
not an official NT-ware client.

**Default mode captures PDFs locally; it does not upload or physically print.**
Cloud upload is opt-in and follows the secure PDF multipart path recovered
from the macOS SmartClient. A separate [Qt WebEngine login app](login-app/README.md)
can obtain tenant OAuth credentials and generate this server's configuration.
Live tenant interoperability has not been verified.

## Build and install on Arch Linux

Generate the local source archive from the current committed revision, then
build the package; no nonexistent GitHub release is downloaded:

```sh
bash scripts/make-source.sh
makepkg -si
sudo systemctl enable --now uniflow-ipp.service
sudo /usr/share/uniflow-ipp/scripts/install-cups-queue.sh
lp -d uniflow_secure document.pdf
```

The service runs as `uniflow-ipp`. Configuration is in
`/etc/uniflow-ipp/config.toml`, documents are in `/var/spool/uniflow-ipp`, and
tokens are in `/var/lib/uniflow-ipp`. Documents and tokens are owner-readable
only. The server accepts loopback IP addresses only, with a 100 MiB request
limit. The queue installer updates existing queues without deleting them.

For a nondefault queue or port, edit the server configuration and pass matching
values to the installer:

```sh
sudo /usr/share/uniflow-ipp/scripts/install-cups-queue.sh \
    my_queue "My Secure Print" ipp://127.0.0.1:9631/ipp
```

Run from source without installing a service:

```sh
go run ./cmd/uniflow-ipp-server --config config.toml
```

Set `server.spool_dir` to a writable directory first.

## Experimental cloud upload

Use the companion GUI to log in from your tenant plist and export configuration:

```sh
uv run --project login-app uniflow-login --plist /path/to/.tenantcfg.plist
```

It supports current-user and packaged-system-service exports. The following
manual provisioning instructions remain available for existing credentials.

The registered **management-server hostname**, **user UUID**, and a valid
tenant-issued **Bearer token** must be provisioned through an authorized
tenant workflow. The macOS client initializes the management server to the
plist's tenant hostname; override that default only when required by the tenant.

Set `cloud.management_server`, `cloud.user_id`, and `cloud.token_path`.
Provision a token file owned by the service account with mode `0600`:

```json
{
  "access_token": "<tenant-issued access token>",
  "token_type": "Bearer",
  "expires_at": "<actual expiry as an RFC3339 UTC timestamp>"
}
```

Optional refresh support also needs `refresh_token` in that file and the
tenant's actual `token_endpoint`, `oauth_client_id`, `oauth_client_secret`
(if required), and `oauth_scope` in the configuration. Protect configuration
files containing client secrets, including from other local users. Tokens
with unknown expiration are refreshed or rejected, never reused indefinitely.
Refresh responses are persisted atomically with mode `0600`.
For credential-provisioned SmartClient refreshes with a client secret, a
missing/empty `token_type` is normalized to `Bearer` with a compatibility
warning, matching the native client. Explicit unsupported types remain invalid.
Provisioned token files must still explicitly specify `Bearer`.

Only then set `cloud.enabled = true` and restart the service. Invalid
configuration or unavailable credentials stop startup instead of silently
falling back to local capture.

The upload is a streamed POST to:

```text
https://<management-server>/api/v1/smartclient/printjob/<user-UUID-without-hyphens>
```

Parts are `metadata` (`{}`), `filecontent` (the original PDF), and `finishings`
(JSON). Copies, duplex, color, staple, punch, and media source are mapped from
IPP attributes. The expected `X-ClientType: SmartClientForMac` header is
retained as a compatibility choice, not proof of Linux support by the service.

An optional `--authorize` command supports explicitly configured RFC 8628
device providers. This is **not** the SmartClient's native registration flow;
no Microsoft endpoint, client ID, or scope is guessed. Use it only when the
tenant confirms such a flow issues a token for this API:

```sh
sudo -u uniflow-ipp uniflow-ipp-server \
    --config /etc/uniflow-ipp/config.toml --authorize
```

The GUI implements `/Home/SmartClientRegistration`, a generated RSA key,
the tenant's encrypted authorization response, and an OAuth authorization-code
exchange. The management-server default is the plist's tenant hostname,
matching the macOS client. Continuous peer-registration heartbeats and peer
credential renewal remain unimplemented.

## Job behavior and limitations

Local-only jobs are reported as **held**, not printed. Uploaded jobs are
reported as completed only after an HTTP success response; this means the
cloud accepted the transfer, not that an MFD printed it. Failures return an
IPP server error and retain the PDF, with an aborted state visible in job
queries. There are no automatic upload retries or redirects, preventing
silent resubmission or document forwarding to unexpected hosts.

Spool PDFs survive daemon restarts; job history and IPP IDs are currently
in-memory only. There is no automatic cleanup, retry queue, cancellation,
Create-Job/Send-Document support, or physical-printer status tracking.
Monitor spool disk usage. CUPS may retry failed submissions independently;
inspect retained jobs before retrying an upload whose remote outcome is
uncertain.

## Development

```sh
go test -race ./...
go vet ./...
cupstestppd -q ppd/uniflow-ipp.ppd
```

The tests exercise multipart framing and document bytes with a local HTTP
server, token expiration/refresh/polling, private persistence, malformed IPP
messages, finishing mappings, spool failures, and concurrent jobs. They do
not authenticate against or submit documents to a live tenant.
