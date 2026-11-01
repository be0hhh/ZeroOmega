package main

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"time"

	awgconn "github.com/amnezia-vpn/amneziawg-go/v3/conn"
	awgdevice "github.com/amnezia-vpn/amneziawg-go/v3/device"
	awgtun "github.com/amnezia-vpn/amneziawg-go/v3/tun"
	"github.com/amnezia-vpn/amneziawg-go/v3/tun/netstack"
	wgconn "golang.zx2c4.com/wireguard/conn"
	wgdevice "golang.zx2c4.com/wireguard/device"
	wgtun "golang.zx2c4.com/wireguard/tun"
)

type engine interface {
	DialContext(context.Context, string, string) (net.Conn, error)
	LastHandshake() time.Time
	Close()
}
type wgDevice interface {
	IpcSet(string) error
	IpcGet() (string, error)
	Up() error
	Close()
}
type tunnel struct {
	dev    wgDevice
	stack  *netstack.Net
	config *Config
}
type standardTUN struct {
	awgtun.Device
	events chan wgtun.Event
}

func wrapStandard(t awgtun.Device) *standardTUN {
	a := &standardTUN{Device: t, events: make(chan wgtun.Event, 16)}
	go func() {
		defer close(a.events)
		for ev := range t.Events() {
			a.events <- wgtun.Event(ev)
		}
	}()
	return a
}
func (t *standardTUN) Events() <-chan wgtun.Event { return t.events }

func startEngine(c *Config) (engine, error) {
	t, stack, err := netstack.CreateNetTUN(c.Addresses, c.DNS, c.MTU)
	if err != nil {
		return nil, errors.New("could not create tunnel network stack")
	}
	var d wgDevice
	if c.Protocol == "amneziawg" {
		d = awgdevice.NewDevice(t, awgconn.NewDefaultBind(), awgdevice.NewLogger(awgdevice.LogLevelSilent, ""))
	} else {
		d = wgdevice.NewDevice(wrapStandard(t), wgconn.NewDefaultBind(), wgdevice.NewLogger(wgdevice.LogLevelSilent, ""))
	}
	config := append([]string{}, c.Device...)
	config = append(config, "replace_peers=true")
	for _, p := range c.Peers {
		endpoint, err := resolveEndpoint(p.Endpoint)
		if err != nil {
			d.Close()
			return nil, errors.New("could not resolve VPN server endpoint")
		}
		config = append(config, "public_key="+p.Key, "endpoint="+endpoint, "replace_allowed_ips=true")
		config = append(config, p.Lines...)
		for _, prefix := range p.Allowed {
			config = append(config, "allowed_ip="+prefix.String())
		}
	}
	if err := d.IpcSet(strings.Join(config, "\n") + "\n"); err != nil {
		d.Close()
		return nil, errors.New("network core rejected configuration; verify protocol parameters")
	}
	if err := d.Up(); err != nil {
		d.Close()
		return nil, errors.New("could not start tunnel; check server and local UDP port")
	}
	return &tunnel{dev: d, stack: stack, config: c}, nil
}
func resolveEndpoint(endpoint string) (string, error) {
	host, port, err := net.SplitHostPort(endpoint)
	if err != nil {
		return "", err
	}
	if a, err := netip.ParseAddr(host); err == nil {
		return net.JoinHostPort(a.String(), port), nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	addresses, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil || len(addresses) == 0 {
		return "", errors.New("endpoint DNS lookup failed")
	}
	// Only the server endpoint is resolved outside the tunnel. Target DNS never is.

	chosen := addresses[0]
	for _, a := range addresses {
		if a.Is4() {
			chosen = a
			break
		}
	}
	return net.JoinHostPort(chosen.String(), port), nil
}
func (t *tunnel) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if network != "tcp" {
		return nil, errors.New("only TCP is supported")
	}
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, errors.New("invalid destination")
	}
	var addresses []netip.Addr
	if a, err := netip.ParseAddr(host); err == nil {
		addresses = []netip.Addr{a}
	} else {
		names, err := t.stack.LookupContextHost(ctx, host)
		if err != nil {
			return nil, errors.New("tunnel DNS lookup failed")
		}
		for _, name := range names {
			if a, e := netip.ParseAddr(name); e == nil {
				addresses = append(addresses, a)
			}
		}
	}
	allowed := make([]netip.Addr, 0, len(addresses))
	for _, a := range addresses {
		if t.config.allowed(a) {
			allowed = append(allowed, a)
		}
		if len(allowed) == 16 {
			break
		}
	}
	return dialCandidates(ctx, allowed, port, t.stack.DialContext)
}

// Stagger candidate addresses inside the tunnel so a blackholed IPv6 route
// cannot consume the entire request deadline before a working IPv4 address.
func dialCandidates(ctx context.Context, addresses []netip.Addr, port string, dial dialFunc) (net.Conn, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	type result struct {
		conn net.Conn
		err  error
	}
	results := make(chan result, len(addresses))
	for i, a := range addresses {
		go func(i int, a netip.Addr) {
			timer := time.NewTimer(time.Duration(i) * 250 * time.Millisecond)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				results <- result{err: ctx.Err()}
				return
			case <-timer.C:
			}
			c, err := dial(ctx, "tcp", net.JoinHostPort(a.String(), port))
			results <- result{c, err}
		}(i, a)
	}
	for completed := 0; completed < len(addresses); completed++ {
		r := <-results
		if r.err == nil && r.conn != nil {
			cancel()
			remaining := len(addresses) - completed - 1
			go func() {
				for i := 0; i < remaining; i++ {
					r := <-results
					if r.conn != nil {
						r.conn.Close()
					}
				}
			}()
			return r.conn, nil
		}
	}

	return nil, errors.New("destination unavailable through tunnel or outside AllowedIPs")
}
func (t *tunnel) LastHandshake() time.Time {
	raw, err := t.dev.IpcGet()
	if err != nil {
		return time.Time{}
	}
	var newest int64
	for _, line := range strings.Split(raw, "\n") {
		if v, ok := strings.CutPrefix(line, "last_handshake_time_sec="); ok {
			sec, _ := strconv.ParseInt(v, 10, 64)
			if sec > newest {
				newest = sec
			}
		}
	}
	if newest == 0 {
		return time.Time{}
	}
	return time.Unix(newest, 0)
}
func (t *tunnel) Close() { t.dev.Close() }

// Validate AWG cross-field constraints while the device remains down, without
// resolving endpoints, opening sockets or sending packets.
func validateCoreConfig(c *Config) error {
	if c.Protocol != "amneziawg" {
		return nil
	}
	t, _, err := netstack.CreateNetTUN(c.Addresses, c.DNS, c.MTU)
	if err != nil {
		return errors.New("invalid tunnel interface addresses")
	}
	d := awgdevice.NewDevice(t, awgconn.NewDefaultBind(), awgdevice.NewLogger(awgdevice.LogLevelSilent, ""))
	defer d.Close()
	lines := make([]string, 0, len(c.Device))
	for _, line := range c.Device {
		if !strings.HasPrefix(line, "listen_port=") {
			lines = append(lines, line)
		}
	}
	if err := d.IpcSet(strings.Join(lines, "\n") + "\n"); err != nil {
		return errors.New("AWG core rejected protocol parameters; check padding and header settings")
	}
	return nil
}
