package web

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net"
	"os"
	"strings"
	"sync/atomic"
)

// Android has no /etc/resolv.conf, so the app names the network's DNS servers
// in INK_DNS, comma-separated. Where the tablet's Private DNS names a host,
// INK_DOT carries it and every lookup goes over TLS to that host.
func init() {
	servers := strings.FieldsFunc(os.Getenv("INK_DNS"), func(r rune) bool { return r == ',' })
	if len(servers) > 0 {
		net.DefaultResolver = resolver(servers, os.Getenv("INK_DOT"), nil)
	}
}

// resolver returns a resolver asking servers, each an address with or without
// a port. Every lookup asks the same server until it fails to answer, then
// the next, so a server that is down costs one attempt. With dot set, it
// speaks DNS over TLS to port 853 and checks the certificate names dot,
// against roots or the system's.
func resolver(servers []string, dot string, roots *x509.CertPool) *net.Resolver {
	var current atomic.Uint32
	return &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			at := current.Load()
			// Lookups running side by side move on once, not once each.
			next := func() { current.CompareAndSwap(at, at+1) }
			server := servers[int(at)%len(servers)]
			var d net.Dialer
			if dot == "" {
				c, err := d.DialContext(ctx, network, withPort(server, "53"))
				if err != nil {
					next()
					return nil, err
				}
				// Go frames DNS by whether the connection reads packets.
				if pc, ok := c.(packetConn); ok {
					return &packetFailover{pc, next}, nil
				}
				return &failover{Conn: c, next: next}, nil
			}
			// A stream that is not a packet connection makes Go frame DNS as
			// over TCP, which is the framing DNS over TLS takes.
			tlsd := tls.Dialer{NetDialer: &d, Config: &tls.Config{ServerName: dot, RootCAs: roots, MinVersion: tls.VersionTLS12}}
			c, err := tlsd.DialContext(ctx, "tcp", withPort(server, "853"))
			if err != nil {
				next()
				return nil, err
			}
			return &failover{Conn: c, next: next}, nil
		},
	}
}

// failover is a connection to one server that moves the lookups on to the
// next once a read from it fails, a timeout included, or it answers that it
// failed or refuses. Over a stream each answer opens on its 2-byte length.
type failover struct {
	net.Conn
	next func()
	head []byte
}

func (f *failover) Read(b []byte) (int, error) {
	n, err := f.Conn.Read(b)
	if len(f.head) < 6 {
		f.head = append(f.head, b[:min(n, 6-len(f.head))]...)
		if len(f.head) == 6 && failed(f.head[2:]) {
			f.next()
		}
	}
	if err != nil {
		f.next()
	}
	return n, err
}

// failed reports whether a DNS answer's header says the server failed,
// SERVFAIL, or refused, REFUSED: a router whose own upstream is down.
func failed(header []byte) bool {
	rcode := header[3] & 0x0F
	return rcode == 2 || rcode == 5
}

type packetConn interface {
	net.Conn
	net.PacketConn
}

// packetFailover is failover over UDP, still a packet connection to Go.
type packetFailover struct {
	packetConn
	next func()
}

func (f *packetFailover) Read(b []byte) (int, error) {
	n, err := f.packetConn.Read(b)
	if err != nil || n >= 4 && failed(b[:n]) {
		f.next()
	}
	return n, err
}

func withPort(server, port string) string {
	if _, _, err := net.SplitHostPort(server); err == nil {
		return server
	}
	return net.JoinHostPort(server, port)
}
