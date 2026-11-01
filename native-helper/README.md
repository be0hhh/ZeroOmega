# ZeroOmega WG Windows companion

The companion is a single Windows executable controlled by the extension through
Native Messaging (`io.github.zeroomega.wg`). It runs a shared, current-user broker
so separate Chrome and Firefox native hosts can use the same tunnel profiles.
There is no system VPN interface, route change, HTTP control endpoint, telemetry,
or separate application window.

## Build and check

Requires Go 1.25 or newer with the pinned dependencies in `go.mod`/`go.sum`.

```sh
go test -race ./...
GOOS=windows GOARCH=amd64 go test -c -o zeroomega-native-tests.exe
GOOS=windows GOARCH=amd64 go build -trimpath -ldflags="-s -w -H=windowsgui" -o zeroomega-wg.exe .
```

Run the Windows test executable on Windows to verify DPAPI and shared named-pipe
broker operation. The Windows tests use temporary data and a unique pipe; they do
not register a native host or modify browser/Windows proxy settings. The encrypted
local-peer tests exercise both real WireGuard and AmneziaWG handshakes, tunneled
DNS, TCP data and AllowedIPs enforcement without contacting an Internet server.

The standard WireGuard device and AmneziaWG device use a shared userspace netstack
through a small `tun.Device` event adapter. gVisor is pinned to the version
supported by the AmneziaWG netstack rather than importing two incompatible
versions into the process. No Wintun driver installation is needed.

## Native protocol (version 1)

Input and output are JSON messages prefixed by a four-byte little-endian length.
Messages are limited to 1 MiB and configuration text to 256 KiB. The request `id`
is a nonempty string of at most 128 characters.

```json
{"id":"1","command":"version"}
{"id":"2","command":"import","config":"[Interface]\n..."}
{"id":"3","command":"status","configId":"32-lowercase-hex-digits"}
```

Commands: `version`, `import`, `connect`, `disconnect`, `status`, `delete`.
Import takes optional `configId` to replace an existing configuration without
changing its local port. Successful responses are `{id,ok:true,result}`; errors
are `{id,ok:false,error:{code,message}}`. Version returns
`{protocolVersion:1,version}`. Profile operations return
`{configId,protocol,port,state,error?}`; delete returns `{configId}`.

A profile's loopback TCP port is allocated once, encrypted in the store and never
reassigned to another profile, even after deletion. The broker reserves it while
the profile is disconnected and rejects SOCKS requests until the tunnel starts.
Failure to bind the assigned port is an error, not permission to replace another
listener. The extension must retain its proxy rule without a direct fallback.
`connecting` means the device is up but no recent authenticated handshake has
completed; the first routed request initiates traffic. `connected` requires a
handshake within the preceding 180 seconds. This is handshake freshness, not a
promise that every destination is reachable.

## Configuration support

- Native `[Interface]` / `[Peer]` WireGuard and AmneziaWG `.conf` files, including
  multiple peers, IPv4/IPv6, preshared keys and persistent keepalives.
- AmneziaWG junk packets, header ranges, S1–S4, custom I1–I5 packets, AWG 3 header
  protection and timing parameters understood by the pinned core.
- `RandomTrailers` and `DisableCookies` accept `on/off`, `true/false`, and `1/0`
  without case sensitivity; values are normalized for the network core.
- Signature tags `b`, `t`, `r`, `rc`, `rd`. Other tags are explicitly rejected.
- Allocation limits: up to 128 junk packets, junk/padding/custom packet sizes up
  to 9000 bytes; `Jmin` must not exceed `Jmax`. Unsupported parameters are rejected.
- DNS values must be IP addresses reachable through `AllowedIPs`. Without an
  explicit DNS setting, `1.1.1.1` is used through the tunnel and must be allowed.
- Only the VPN server's endpoint hostname is resolved by the operating system.
  Destination hostnames are resolved inside the tunnel. Every destination address
  is checked against `AllowedIPs`; no ordinary outbound TCP fallback exists.
- Only SOCKS5 TCP CONNECT is supported. UDP ASSOCIATE, BIND, shell hooks,
  `SaveConfig`, system `Table`/routing commands, universal `vpn://` keys and
  Shadowsocks are outside this version.

## Storage and lifecycle

Configuration text and stable port metadata are protected as a single DPAPI
current-user blob at `%LOCALAPPDATA%\ZeroOmegaWG\configs\store.dpapi`, updated
atomically. Responses do not include configuration text or keys. Errors omit
secret values, and protocol stdout is never used for logging.

The broker pipe has a current-user-only DACL and rejects remote pipe clients.
Native bridges also verify the pipe server process belongs to the same Windows
user before forwarding configuration material. Other software running as the
same user has the same authority as this companion; this is not an isolation
boundary against malicious same-user software.

`zeroomega-wg.exe --shutdown` stops the current user's broker and waits for its
process to exit, so an installer can replace the executable safely. It succeeds
if no broker exists. It is an installer command, not a forwarded extension API.
`--broker` is internal; the default entrypoint is the browser's native bridge.
`--start` starts the shared broker if necessary and verifies its protocol health
before returning. `--check` performs the same health check without starting a
missing broker. Both use exit status and keep protocol stdout unused; a running
broker continues after these commands exit. No Windows startup task is created.
Disconnect closes active tunnel connections but keeps the assigned proxy port
reserved and rejecting requests. Closing the extension popup does not stop the
broker. Explicit disconnect and uninstall/shutdown stop active connections.
