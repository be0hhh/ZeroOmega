# ZeroOmega WG for Windows

Explicit Disconnect pauses automatic reconnection for the current background
session. Restarting the browser reconnects tunnels referenced by the active
profile. Switching profiles does not stop tunnels potentially shared by the
other browser. Standalone PAC exports cannot authorize WG/AWG destinations and
keep them blocked; move settings and reimport the original `.conf` instead.

This personal ZeroOmega fork adds WireGuard and AmneziaWG profiles while keeping
the existing proxy profiles and auto switch interface. The extension controls a
small local companion without a separate VPN window. You provide the server and
its configuration file.

## One file to install and share

1. Run **`ZeroOmega-WG-Setup.exe`**. No manual extraction or PowerShell command
   is required. Share this same EXE with another person to give them the client.
2. Setup copies the companion and extension to `%LOCALAPPDATA%\ZeroOmegaWG`,
   registers Native Messaging for the current user, starts the companion and
   checks its response. Administrator rights and persistent PowerShell policy
   changes are not required. Windows may ask you to confirm running an unsigned EXE.
3. **Chrome:** the opened help page shows the full installed path. Open
   `chrome://extensions`, enable Developer mode, select **Load unpacked**, then
   choose **`%LOCALAPPDATA%\ZeroOmegaWG\chrome`**, using your full installed path
   in the picker. The extension is named **ZeroOmega WG**.
   Disable the original ZeroOmega so the two extensions do not compete for
   browser proxy settings.
4. You can delete the downloaded EXE after installation. Files are in the permanent
   application directory. A **ZeroOmega WG** Start menu shortcut opens setup help.
5. **Firefox:** `firefox/zeroomega-wg-unsigned.xpi` in the installed application
   directory is not signed. For permanent
   installation in normal Firefox, submit it to Mozilla for **unlisted / self
   distribution** signing, then install the returned signed XPI with the add-on
   manager's **Install Add-on From File** command. No public catalog listing is
   required. You can share the signed XPI with other users.

For a temporary Firefox test, extract the XPI and load its `manifest.json` with
`about:debugging` → **This Firefox** → **Load Temporary Add-on**. The temporary
installation disappears after restarting Firefox; it does not replace signing.

See Mozilla's [signing and distribution guide](https://extensionworkshop.com/documentation/publish/signing-and-distribution-overview/).
A Mozilla account, submission and signing are not included in this kit.

The ZIP kit is also available for manual installation: extract it and run
`powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\install.ps1`.
It copies the extension to the same permanent application directory.
For unattended setup without opening windows, use
`ZeroOmega-WG-Setup.exe --NoOpen` or `install.ps1 -NoOpen`.
The companion still starts and is checked.

## Connect and configure auto switch

1. Create a **WireGuard / AmneziaWG** profile in the extension settings.
2. Import a `.conf` file or paste its contents. The companion detects the protocol
   from its parameters. Save the profile and apply the settings changes.
3. Select the profile from the extension menu to send browser web requests through
   it. **Connected** requires a successful exchange with the server; creating a
   profile alone does not prove that the server is reachable.
4. For selected sites, use **auto switch**, add a domain/host-pattern rule, and
   select your tunnel profile. Set the default profile to **Direct**, apply the
   changes, and enable auto switch.

Rules match the hostname of each individual request. Add separate rules for API,
image, login or other third-party domains when needed. Different sites can use
different server profiles simultaneously.

If the selected tunnel or companion fails, its requests fail instead of switching
to a direct connection. Other domains continue following their own rules. Removing
or disabling the extension hands proxy control back to the browser; this is not a
system VPN kill switch.

## Configurations, updates and removal

Configurations are stored in `%LOCALAPPDATA%\ZeroOmegaWG\configs`, protected with
Windows DPAPI for the current Windows user. Private keys are not included in the
normal ZeroOmega settings export. An export transfers rules and profile links;
import the original `.conf` again on another computer. Keep that file privately
and do not attach it to public bug reports.

To update, fully close Chrome and Firefox and run the new `ZeroOmega-WG-Setup.exe`.
It preserves saved configurations, updates the same installed directory, and starts
the companion. Reopen Chrome and reload the extension at `chrome://extensions`.
If a browser still holds the companion executable open, setup asks you to close
the browsers and retry. For Firefox, install the new signed version.
This fork uses pinned extension IDs.

Open PowerShell in `%LOCALAPPDATA%\ZeroOmegaWG` and remove the application with:

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\uninstall.ps1
```

Saved configurations are preserved by default. Add `-RemoveConfigs` to delete
them. Installed extension files and the shortcut are removed; remove the extension
entry separately in each browser. Once the extension is disabled or removed,
browser routing returns to normal. There is no system kill switch after removal.

## First-version boundaries

- Windows x64 only. Chrome and Firefox share one companion on a computer, while
  their extension settings remain independent.
- WireGuard and AmneziaWG `.conf` files. Generic Amnezia `vpn://` keys, Shadowsocks
  and authenticated SOCKS5 in Chrome are outside this patch.
- Unsupported configuration fields return errors. Configuration commands such as
  `PreUp`, `PostUp`, `PreDown` and `PostDown` are never executed.
- Browser web requests only: WebRTC, system applications and connections bypassing
  browser proxy settings are outside the tunnel guarantee. System routes are not
  modified.
- End-to-end verification requires working test servers. Builds and local tests
  alone do not establish connectivity to your server.
- The kit is prepared for self distribution and has no Mozilla or Authenticode
  signature or store publication.

## Build from source

Build commands target Linux/WSL; the resulting kit installs on Windows.
Requirements: Node.js 20+ (build verified with Node.js 22), npm, Python 3, Git,
Go 1.25+ and dependency registry access. From the repository root:

```sh
python3 scripts/build-windows-kit.py --install-deps
```

Dependencies stay inside the project. The script links local modules without
global `npm link`, builds the extension, and cross-compiles the Windows x64
companion. Outputs: `dist/ZeroOmega-WG-Setup.exe` for single-file sharing and
`dist/zeroomega-wg-windows-x64.zip` for manual setup. The EXE embeds that ZIP,
extracts it to a temporary directory, installs the application, then removes the
temporary files. Omit `--install-deps` when dependencies are installed.
Use `--go` for an explicit Go executable or
`--skip-extension-build` to package an already built extension.

The kit includes `COPYING`, `AUTHORS`, dependency notices,
`source/zeroomega-wg.patch`, and `source/zeroomega-wg-source.zip` containing the
fork and the native companion's Go dependency sources. To rebuild the companion
from that source snapshot, run `go build -mod=vendor .` in `native-helper`.
`BUILD-INFO.json` records versions and the patch hash.

Run packaging checks with:

```sh
python3 scripts/test_windows_kit.py
(cd distribution/setup && go test ./...)
```
