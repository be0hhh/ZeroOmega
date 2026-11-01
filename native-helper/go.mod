module io.github.zeroomega/wg

go 1.25.0

require (
	github.com/Microsoft/go-winio v0.6.2
	github.com/amnezia-vpn/amneziawg-go/v3 v3.1.20260828
	golang.org/x/crypto v0.42.0
	golang.org/x/net v0.44.0
	golang.org/x/sys v0.36.0
	golang.zx2c4.com/wireguard v0.0.0-20250521234502-f333402bd9cb
)

require (
	github.com/google/btree v1.1.3 // indirect
	golang.org/x/time v0.9.0 // indirect
	golang.zx2c4.com/wintun v0.0.0-20230126152724-0fa3db229ce2 // indirect
	gvisor.dev/gvisor v0.0.0-20250503011706-39ed1f5ac29c // indirect
)

// Only amneziawg-go's netstack is used; its supported gVisor version is shared
// with standard WireGuard through a tun.Device adapter.
replace gvisor.dev/gvisor => gvisor.dev/gvisor v0.0.0-20231202080848-1f7806d17489
