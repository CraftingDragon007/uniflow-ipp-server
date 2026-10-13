# Obtain `.tenantcfg.plist` from uniFLOW Online

The login app needs the tenant-specific `.tenantcfg.plist` supplied with a
**macOS uniFLOW SmartClient installer**. It identifies the tenant and carries
installer settings; a generic SmartClient package or a plist made from the
tenant URL is not a substitute. You do not need to install the macOS client
to extract this file on Linux.

> This independent project is not affiliated with, endorsed by, or supported
> by NT-ware. Use an installer provided by your own uniFLOW Online tenant.

## 1. Download your tenant's Mac SmartClient installer

Sign in to your organization's uniFLOW Online portal. If **Start printing**
(or its translated equivalent) offers a macOS SmartClient download, save the
complete `.iso` file. The option's name and availability depend on the tenant.

If the download is not offered to you, ask your uniFLOW Online administrator
for the tenant-specific **Mac SmartClient ISO**. Administrators can manage
SmartClient installers in the uniFLOW SmartClient extension under **Installer
configuration and creation** (a path documented for Windows in NT-ware's
[release notes](https://www.uniflowonline.com/fileadmin/user_upload/uniflow/downloads/uniflow_online_2022.2.1_release_notes.pdf)).
Download the installer for your tenant and its
secure-print configuration, not an installer from another organization.

NT-ware's [SmartClient overview](https://www.uniflowonline.com/en/technology-integrations/uniflow-smartclient/)
describes the installer management dashboard; its
[training guide](https://www.uniflowonline.com/fileadmin/user_upload/uniflow/downloads/training_guide_uniflow_online.pdf)
covers creating Windows and Mac installers. Menu wording may vary by tenant
and product version.

## 2. Extract the hidden plist on Linux

The Mac ISO examined for this project contained both `SmartClientForMac.pkg`
and `.tenantcfg.plist` at the top level. The leading dot makes the plist
hidden in many file managers. Use `bsdtar` (from `libarchive` on Arch Linux)
to check your downloaded ISO:

```sh
bsdtar -tf "/path/to/your-SmartClient-Mac.iso" | grep -Fx .tenantcfg.plist
```

Then extract only the plist into a private directory:

```sh
umask 077
mkdir -p "$HOME/.local/share/uniflow-ipp"
chmod 700 "$HOME/.local/share/uniflow-ipp"
bsdtar -xOf "/path/to/your-SmartClient-Mac.iso" .tenantcfg.plist \
  > "$HOME/.local/share/uniflow-ipp/.tenantcfg.plist"
chmod 600 "$HOME/.local/share/uniflow-ipp/.tenantcfg.plist"
```

If the first command finds no `.tenantcfg.plist`, check that you downloaded
the **Mac ISO**, rather than a standalone `.pkg`, a Windows installer, or a
different uniFLOW workflow. Ask your tenant administrator for the matching
installer; do not invent or reuse another tenant's plist.

## 3. Use it with the login app

From this repository's root directory:

```sh
uv run --project login-app uniflow-login \
  --plist "$HOME/.local/share/uniflow-ipp/.tenantcfg.plist"
```

Complete your tenant's sign-in. For the packaged systemd service, select
**Export paths for the packaged system service** before exporting the server
configuration. Continue with the [main installation guide](README.md#install-and-connect).

The plist and downloaded ISO are tenant-specific. Keep them private and do
not commit or attach them to public issues. The generated `config.toml` and
`token.json` contain credentials and need even stricter handling.
