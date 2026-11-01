package main

import (
	"bufio"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
)

const maxConfigSize = 256 << 10

type Config struct {
	Protocol  string
	Addresses []netip.Addr
	DNS       []netip.Addr
	MTU       int
	Device    []string
	Peers     []PeerConfig
}
type PeerConfig struct {
	Key, Endpoint string
	Allowed       []netip.Prefix
	Lines         []string
}

var awgKeys = map[string]string{
	"jc": "jc", "jmin": "jmin", "jmax": "jmax", "s1": "s1", "s2": "s2", "s3": "s3", "s4": "s4",
	"h1": "h1", "h2": "h2", "h3": "h3", "h4": "h4", "i1": "i1", "i2": "i2", "i3": "i3", "i4": "i4", "i5": "i5",
	"headerprotectionkey": "header_protection_key", "contentpaddingaddition": "content_padding_addition",
	"rekeyaftertime": "rekey_after_time", "rekeytimeout": "rekey_timeout", "rejectaftertime": "reject_after_time",
	"keepalivetimeout": "keepalive_timeout", "maxhandshakeattempts": "max_handshake_attempts",
	"randomtrailers": "random_trailers", "disablecookies": "disable_cookies",
}

func decodeKey(value string) (string, error) {
	b, err := base64.StdEncoding.Strict().DecodeString(value)
	if err != nil || len(b) != 32 {
		return "", errors.New("key must be a base64-encoded 32-byte value")
	}
	zero := true
	for _, v := range b {
		if v != 0 {
			zero = false
			break
		}
	}
	if zero {
		return "", errors.New("key must not be zero")
	}
	return hex.EncodeToString(b), nil
}
func parseUint(value string, bits int) error {
	_, err := strconv.ParseUint(value, 10, bits)
	if err != nil {
		return errors.New("invalid unsigned integer")
	}
	return nil
}
func parseRange(value string, bits int) error {
	a, b, has := strings.Cut(value, "-")
	if err := parseUint(a, bits); err != nil {
		return err
	}
	if !has {
		return nil
	}
	if err := parseUint(b, bits); err != nil {
		return err
	}
	av, _ := strconv.ParseUint(a, 10, bits)
	bv, _ := strconv.ParseUint(b, 10, bits)
	if av > bv {
		return errors.New("invalid range")
	}
	return nil
}
func parseConfig(raw string) (*Config, error) {
	if len(raw) == 0 || len(raw) > maxConfigSize {
		return nil, errors.New("configuration must be between 1 byte and 256 KiB")
	}
	if strings.ContainsRune(raw, 0) {
		return nil, errors.New("configuration contains invalid characters")
	}
	c := &Config{Protocol: "wireguard", MTU: 1420}
	section := ""
	seenInterface := false
	seen := map[string]bool{}
	hasPrivate := false
	awgValues := map[string]string{}
	scan := bufio.NewScanner(strings.NewReader(strings.TrimPrefix(raw, "\ufeff")))
	scan.Buffer(make([]byte, 4096), maxConfigSize)
	lineNo := 0
	for scan.Scan() {
		lineNo++
		line := strings.TrimSpace(scan.Text())
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		if line == "" || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			switch strings.ToLower(line) {
			case "[interface]":
				if seenInterface || len(c.Peers) > 0 {
					return nil, fmt.Errorf("line %d: duplicate or misplaced Interface", lineNo)
				}
				seenInterface = true
				section = "interface"
			case "[peer]":
				if !seenInterface || len(c.Peers) >= 32 {
					return nil, fmt.Errorf("line %d: invalid Peer section", lineNo)
				}
				section = "peer"
				c.Peers = append(c.Peers, PeerConfig{})
			default:
				return nil, fmt.Errorf("line %d: unsupported section", lineNo)
			}
			seen = map[string]bool{}
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		key = strings.ToLower(strings.TrimSpace(key))
		val = strings.TrimSpace(val)
		if !ok || section == "" || key == "" || val == "" || seen[key] {
			return nil, fmt.Errorf("line %d: invalid or duplicate setting", lineNo)
		}
		seen[key] = true
		fail := func(err error) (*Config, error) { return nil, fmt.Errorf("line %d: %s", lineNo, err) }
		if section == "interface" {
			if awg, ok := awgKeys[key]; ok {
				c.Protocol = "amneziawg"
				switch {
				case key == "headerprotectionkey":
					v, err := decodeKey(val)
					if err != nil {
						return fail(err)
					}
					val = v
				case key == "randomtrailers" || key == "disablecookies":
					switch strings.ToLower(val) {
					case "true", "1", "on":
						val = "1"
					case "false", "0", "off":
						val = "0"
					default:
						return fail(errors.New("boolean must be on/off, true/false, or 1/0"))
					}
				case strings.HasPrefix(key, "i") && len(key) == 2:
					if err := validateSignature(val); err != nil {
						return fail(err)
					}
				case strings.HasPrefix(key, "h") && len(key) == 2 || key == "contentpaddingaddition" || strings.HasSuffix(key, "time") || strings.HasSuffix(key, "timeout") || key == "maxhandshakeattempts":
					if err := parseRange(val, 32); err != nil {
						return fail(err)
					}
				default:
					if err := parseUint(val, 32); err != nil {
						return fail(err)
					}
				}
				if err := boundAWG(key, val); err != nil {
					return fail(err)
				}
				awgValues[key] = val
				c.Device = append(c.Device, awg+"="+val)
				continue
			}
			switch key {
			case "privatekey":
				v, err := decodeKey(val)
				if err != nil {
					return fail(err)
				}
				c.Device = append(c.Device, "private_key="+v)
				hasPrivate = true
			case "address":
				for _, v := range strings.Split(val, ",") {
					p, err := netip.ParsePrefix(strings.TrimSpace(v))
					if err != nil || p.Addr().IsUnspecified() || p.Addr().IsMulticast() {
						return fail(errors.New("invalid interface address"))
					}
					c.Addresses = append(c.Addresses, p.Addr())
				}
			case "dns":
				for _, v := range strings.Split(val, ",") {
					a, err := netip.ParseAddr(strings.TrimSpace(v))
					if err != nil || a.IsUnspecified() || a.IsMulticast() {
						return fail(errors.New("DNS must contain IP addresses only"))
					}
					c.DNS = append(c.DNS, a)
				}
			case "mtu":
				n, err := strconv.Atoi(val)
				if err != nil || n < 1280 || n > 9000 {
					return fail(errors.New("MTU must be between 1280 and 9000"))
				}
				c.MTU = n
			case "listenport":
				if err := parseUint(val, 16); err != nil {
					return fail(err)
				}
				c.Device = append(c.Device, "listen_port="+val)
			default:
				return fail(errors.New("unsupported interface setting; commands and system routing settings are not accepted"))
			}
		} else {
			p := &c.Peers[len(c.Peers)-1]
			switch key {
			case "publickey":
				v, err := decodeKey(val)
				if err != nil {
					return fail(err)
				}
				p.Key = v
			case "presharedkey":
				v, err := decodeKey(val)
				if err != nil {
					return fail(err)
				}
				p.Lines = append(p.Lines, "preshared_key="+v)
			case "endpoint":
				host, port, err := net.SplitHostPort(val)
				if err != nil || host == "" || len(host) > 253 || strings.ContainsAny(host, "\t\r\n /\\") || parseUint(port, 16) != nil || port == "0" {
					return fail(errors.New("Endpoint must be a hostname or IP and a nonzero port"))
				}
				p.Endpoint = val
			case "allowedips":
				for _, v := range strings.Split(val, ",") {
					prefix, err := netip.ParsePrefix(strings.TrimSpace(v))
					if err != nil {
						return fail(errors.New("invalid AllowedIPs prefix"))
					}
					p.Allowed = append(p.Allowed, prefix.Masked())
				}
			case "persistentkeepalive":
				if c.Protocol == "amneziawg" {
					if err := parseRange(val, 16); err != nil {
						return fail(err)
					}
				} else {
					if err := parseUint(val, 16); err != nil {
						return fail(err)
					}
				}
				p.Lines = append(p.Lines, "persistent_keepalive_interval="+val)
			default:
				return fail(errors.New("unsupported peer setting"))
			}
		}
	}
	if scan.Err() != nil {
		return nil, errors.New("configuration line is too long")
	}
	if !seenInterface || !hasPrivate || len(c.Addresses) == 0 || len(c.Peers) == 0 {
		return nil, errors.New("configuration requires Interface PrivateKey, Address, and at least one Peer")
	}
	if c.Protocol == "amneziawg" {
		jmin, _ := strconv.ParseUint(awgValues["jmin"], 10, 32)
		jmax, _ := strconv.ParseUint(awgValues["jmax"], 10, 32)
		if jmin > jmax {
			return nil, errors.New("Jmin must not exceed Jmax")
		}
	}
	keys := map[string]bool{}
	for _, p := range c.Peers {
		if p.Key == "" || p.Endpoint == "" || len(p.Allowed) == 0 {
			return nil, errors.New("each Peer requires PublicKey, Endpoint and AllowedIPs")
		}
		if keys[p.Key] {
			return nil, errors.New("duplicate peer public key")
		}
		keys[p.Key] = true
	}
	if len(c.DNS) == 0 {
		c.DNS = []netip.Addr{netip.MustParseAddr("1.1.1.1")}
	}
	for _, a := range c.DNS {
		if !c.allowed(a) {
			return nil, errors.New("DNS address is outside AllowedIPs; specify a reachable DNS IP")
		}
	}
	return c, nil
}
func (c *Config) allowed(addr netip.Addr) bool {
	for _, p := range c.Peers {
		for _, prefix := range p.Allowed {
			if prefix.Contains(addr.Unmap()) {
				return true
			}
		}
	}
	return false
}

// Bound allocation-driving AWG fields before passing them to the network core.
// Values outside this supported subset are rejected rather than silently altered.
func boundAWG(key, value string) error {
	max := uint64(0)
	switch key {
	case "jc":
		max = 128
	case "jmin", "jmax", "s1", "s2", "s3", "s4", "contentpaddingaddition":
		max = 9000
	}
	if max == 0 {
		return nil
	}
	_, upper, has := strings.Cut(value, "-")
	if !has {
		upper = value
	}
	n, err := strconv.ParseUint(upper, 10, 32)
	if err != nil || n > max {
		return errors.New("AWG packet allocation exceeds supported limits (128 junk packets, 9000 bytes each)")
	}
	return nil
}
func validateSignature(value string) error {
	if len(value) > 18000 {
		return errors.New("signature packet is too large")
	}
	total := 0
	for strings.TrimSpace(value) != "" {
		value = strings.TrimSpace(value)
		if !strings.HasPrefix(value, "<") {
			return errors.New("invalid signature packet syntax")
		}
		end := strings.IndexByte(value, '>')
		if end < 0 {
			return errors.New("invalid signature packet syntax")
		}
		fields := strings.Fields(value[1:end])
		value = value[end+1:]
		if len(fields) == 0 || len(fields) > 2 {
			return errors.New("invalid signature packet tag")
		}
		size := 0
		switch fields[0] {
		case "t":
			if len(fields) != 1 {
				return errors.New("timestamp signature tag takes no argument")
			}
			size = 4
		case "b":
			if len(fields) != 2 {
				return errors.New("bytes signature tag requires hex bytes")
			}
			b, err := hex.DecodeString(strings.TrimPrefix(fields[1], "0x"))
			if err != nil || len(b) == 0 {
				return errors.New("invalid signature bytes")
			}
			size = len(b)
		case "r", "rc", "rd":
			if len(fields) != 2 {
				return errors.New("random signature tag requires a byte count")
			}
			n, err := strconv.ParseUint(fields[1], 10, 16)
			if err != nil || n > 9000 {
				return errors.New("signature byte count exceeds supported limit")
			}
			size = int(n)
		default:
			return errors.New("unsupported signature tag; supported tags are b, t, r, rc, rd")
		}
		total += size
		if total > 9000 {
			return errors.New("signature packet exceeds 9000 bytes")
		}
	}
	if total == 0 {
		return errors.New("signature packet must not be empty")
	}
	return nil
}
