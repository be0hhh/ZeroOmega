# Validation of ZeroOmega WG 3.5.2.1

Base: `zero-peak/ZeroOmega` commit `c367db2` (3.5.2). Checks were run under
Linux/WSL with Node.js 22 and Go 1.25, with separate Windows runtime checks.

## Passed

- Full extension build, including CoffeeScript lint/compilation, Jade, browser
  bundles, static assets and English/Russian translations.
- PAC suite: 150 passing. Updated one stale upstream SOCKS5 expectation to match
  the unchanged default behavior and separately test explicit legacy export.
- Browser integration: 11 passing; editor controller: 9 passing. They cover
  runtime config/port authorization, cached PAC revocation, native loss and
  same-tick response races, Firefox remote DNS, stale config recovery and
  excluding configuration bodies from profile metadata.
- Go suite with the race detector and `go vet`: passed. Real encrypted local
  WireGuard and AmneziaWG peers exercise DNS, TCP and AllowedIPs restrictions.
- Actual Windows test executable: passed, including DPAPI protected storage,
  shared named-pipe broker hosts, restart with stable ports and server identity
  validation. Windows GUI-subsystem production executable also passed framed
  stdio, broker launch and synchronous shutdown checks. No installer/registry
  registration was performed in that initial pass; the follow-up below includes
  an actual current-user installation.
- Playwright with the built extension in Chromium: profile creation,
  missing-helper guidance, import/clear, apply, active auto switch policy,
  disconnect and secret-free settings passed. Native messages were simulated in
  this UI test; encrypted protocol tests use the real cores separately.
- The same browser test checked actual HTTP navigation to a local test server:
  the unmatched domain loaded directly; the selected domain failed through its
  blocked proxy and never reached the server directly.
- Editor screenshots inspected at desktop and narrow desktop widths; original
  ZeroOmega styling retained. Mechanical UI detector reported no findings.
- Windows packaging unit tests: 7 passing. Both installer scripts parsed with
  Windows PowerShell; browser IDs match their native host registrations.
- Firefox `web-ext lint`: zero errors, 105 warnings, primarily inherited
  library/code patterns and compatibility warnings. This is not Mozilla signing
  approval; review and an account are still needed.

## Existing upstream test failures

The aggregate `omega-build` test command stops in the unchanged `omega-target`
sync suite: 8 passing and 5 failures (two obsolete merge expectations and three
request-push timeouts). An isolated extraction of pristine `HEAD` with the same
dependencies reproduces the identical 8/5 result. No sync implementation was
changed for this feature. The remaining feature suites were run separately and
passed as listed above.

## One-file installer follow-up

- Added a standalone Windows GUI installer embedding the complete distribution
  ZIP. It installs the companion, browser assets, source and help in the current
  user's `%LOCALAPPDATA%\\ZeroOmegaWG`, plus a Start Menu help shortcut.
- Installer extraction race tests and independent review passed, covering unsafe
  archive paths, links, Windows filename aliases, case duplicates and size limits.
- New `--start` and `--check` readiness commands verify a framed protocol response;
  Windows tests cover healthy broker readiness and rejected invalid responses.
- Executed the actual single EXE on the user's Windows with `--NoOpen`. It
  upgraded the running companion, registered both native hosts, installed Chrome
  and Firefox files to their permanent locations and returned success. The
  installed companion answered version `0.1.1` and passed `--check`; its broker
  remains running. No browser profiles or enterprise policies were modified.
- Native Linux race tests and vet still pass. The live installation deliberately
  suppressed opening browser windows; browser extension installation is still a
  manual browser action.

## Not verified

- Connections to the user's external servers: no credentials/configs supplied.
- A complete installed Windows Chrome/Firefox session with a VPN server. Chromium
  UI, Windows installation/runtime, and proxy integrations were checked separately.
- Mozilla signature, Authenticode signature or store publication. The bundled
  Firefox XPI is unsigned and needs unlisted signing for permanent installation.

Useful commands from the repository root:

```sh
cd omega-build && npm run build
# Suite commands are run from each package with ../omega-build/node_modules/.bin/grunt test
cd ../native-helper && go test -race ./... && go vet ./...
cd .. && python3 scripts/test_windows_kit.py
# Install Playwright externally or set PLAYWRIGHT_MODULE to its module path:
node scripts/test-browser.cjs
```

The browser smoke test requires a fresh environment without an installed native
helper, and Playwright Chromium with its OS libraries. It uses a temporary
browser profile and leaves the user's normal browser profiles untouched.
