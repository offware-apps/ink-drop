package web

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"os"
	"strings"
	"syscall"
	"time"
)

// With INK_PUBLIC_ONLY set, every connection a stage opens must reach the
// public internet. The tablet app sets it, since any app there can share an
// address with it: a share naming a router or a home server is refused at
// the dial, after DNS and after every redirect. INK_LOCAL names the prefixes
// of the network the tablet sits on, comma-separated, since on IPv6 a home
// network's addresses are global ones.
func init() {
	publicFromEnv()
}

func publicFromEnv() {
	if os.Getenv("INK_PUBLIC_ONLY") == "" {
		return
	}
	var local []netip.Prefix
	for _, s := range strings.FieldsFunc(os.Getenv("INK_LOCAL"), func(r rune) bool { return r == ',' }) {
		if p, err := netip.ParsePrefix(s); err == nil {
			local = append(local, p.Masked())
		}
	}
	d := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second, Control: func(network, address string, c syscall.RawConn) error {
		return public(address, local)
	}}
	http.DefaultTransport.(*http.Transport).DialContext = d.DialContext
}

var (
	// cgnat is the shared address space carriers hand out, which netip does
	// not count as private.
	cgnat = netip.MustParsePrefix("100.64.0.0/10")
	// nat64 prefixes carry an IPv4 address in their last 32 bits.
	nat64 = []netip.Prefix{netip.MustParsePrefix("64:ff9b::/96"), netip.MustParsePrefix("64:ff9b:1::/48")}
)

// public refuses the device itself, a home or office network, a link-local
// or multicast address, an address inside one of the local prefixes, and a
// NAT64 address carrying any of those.
func public(address string, local []netip.Prefix) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return err
	}
	ip = ip.Unmap()
	for _, p := range nat64 {
		if p.Contains(ip) {
			b := ip.As16()
			ip = netip.AddrFrom4([4]byte{b[12], b[13], b[14], b[15]})
		}
	}
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || cgnat.Contains(ip) {
		return fmt.Errorf("%s is not on the public internet", ip)
	}
	for _, p := range local {
		if p.Contains(ip) {
			return fmt.Errorf("%s is on the network the device sits on", ip)
		}
	}
	return nil
}
