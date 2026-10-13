# uniFLOW IPP Server for Linux

An unofficial IPP server that connects a local CUPS queue to uniFLOW Online
Secure Print. It accepts PDF print jobs on a loopback address and can upload
them to the user's cloud queue. The project targets Arch Linux and includes a
desktop [login app](login-app/README.md) for obtaining tenant credentials.

This is an experimental client, not an NT-ware product. Cloud submission has
been verified with one tenant through the packaged systemd service; other
tenants and printing at a physical device have not been tested. The package's
default configuration **holds PDFs locally and does not upload them** until
cloud access is configured.

## Requirements

- Arch Linux on x86-64 with CUPS, `makepkg`, and Go 1.22 or newer for the
  packaged build.
- Python 3.11 or newer and [`uv`](https://docs.astral.sh/uv/) to run the login
  app from source. See its README for an alternative Python installation.
- A uniFLOW Online account and a tenant-provided SmartClient
  `.tenantcfg.plist` for cloud printing.

## Install and connect

1. Build and install the server package from a committed checkout:

   ```sh
   bash scripts/make-source.sh
   makepkg -si
   ```

   `make-source.sh` archives the current Git `HEAD`. Commit source changes
   before packaging them. The package installs the service, sample config,
   CUPS PPD, and queue installer; it does not enable cloud upload.

2. Run the login app and select the tenant plist:

   ```sh
   uv run --project login-app uniflow-login --plist /path/to/.tenantcfg.plist
   ```

   Complete sign-in and MFA. If your tenant opens the system browser, keep
   the login app open until registration finishes. Before clicking **Export
   server configuration**, select **Export paths for the packaged system
   service**. The app exports a private `config.toml` and `token.json` and
   displays commands to install them. Run those commands; the app does not
   make system changes itself.

3. Start the service, create the CUPS queue, and print a test document:

   ```sh
   sudo systemctl enable --now uniflow-ipp.service
   sudo /usr/share/uniflow-ipp/scripts/install-cups-queue.sh
   lp -d uniflow_secure document.pdf
   ```

   Look for the job in your uniFLOW Online secure queue. A completed CUPS job
   means the cloud accepted the upload; it does not mean a device printed it.

For a different queue name or listener port, set the matching values in the
server configuration and pass the same name and URI to the queue installer:

```sh
sudo /usr/share/uniflow-ipp/scripts/install-cups-queue.sh \
    my_queue "My Secure Print" ipp://127.0.0.1:9631/ipp
```

## Configuration and files

The packaged service runs as the `uniflow-ipp` user with these paths:

| Purpose | Path |
| --- | --- |
| Configuration | `/etc/uniflow-ipp/config.toml` |
| OAuth token | `/var/lib/uniflow-ipp/token.json` |
| PDF spool | `/var/spool/uniflow-ipp` |
| IPP endpoint | `ipp://127.0.0.1:8631/ipp` |

The login app's **system service** export uses these token and spool paths.
Its default **current-user** export uses paths under the exported directory
and will not work unchanged with the packaged service, which has home
directory access blocked. The login app shows the required install commands:
configuration is installed as `root:uniflow-ipp` with mode `0640`, and the
token as `uniflow-ipp:uniflow-ipp` with mode `0600`. Both contain credentials;
do not commit or share them.

The sample config has `cloud.enabled = false`. In that mode, jobs are held as
local PDFs only. The login app's export enables cloud submission and includes
the management server, user ID, OAuth settings, and token path supplied by
registration. The server refreshes access tokens and stores replacements
privately. Peer credential renewal still requires signing in again before
those credentials expire.

After changing the installed config or token, restart the service:

```sh
sudo systemctl restart uniflow-ipp.service
sudo journalctl -u uniflow-ipp.service -n 50 --no-pager
```

The server accepts IPP connections only on a loopback IP address and limits
requests to 100 MiB. Cloud upload uses HTTPS and the secure PDF submission
flow. Copies, duplex, color, staple, punch, and media source settings are
mapped from IPP job attributes where supported.

## Job handling and current limits

- A successful cloud response completes the IPP job and removes its local
  spool PDF. A cleanup failure is logged but does not cause a second upload.
- A failed cloud upload returns an IPP error and retains the PDF for
  inspection. Local-only jobs also retain their PDFs. There is no automatic
  cleanup for retained jobs.
- The server does not retry uploads or follow upload redirects. CUPS may retry
  failed submissions independently, so check the cloud queue and retained
  spool files before resubmitting an uncertain job.
- Job history and IPP IDs are in memory and reset on restart. Cancellation,
  `Create-Job`/`Send-Document`, physical printer status, public print, and
  zero-trust workflows are not implemented.

The companion login app implements initial SmartClient registration. Ongoing
peer-registration heartbeats and peer credential renewal are not implemented.
See [login-app/README.md](login-app/README.md) for its setup and limitations.

## Development

Run the server directly with an exported current-user config:

```sh
go run ./cmd/uniflow-ipp-server --config /path/to/export/config.toml
```

Run the Go checks:

```sh
go test -race ./...
go vet ./...
cupstestppd -q ppd/uniflow-ipp.ppd
```

The tests use local fixtures and do not authenticate against a live tenant or
submit a cloud job. The login app has separate tests documented in its README.
