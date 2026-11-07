package web

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"io"
	"net"
	"net/http/httptest"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// answer returns the reply to the DNS query q: example.test at 192.0.2.7.
func answer(t *testing.T, q []byte) []byte {
	var p dnsmessage.Parser
	h, err := p.Start(q)
	if err != nil {
		t.Errorf("query: %v", err)
		return nil
	}
	question, err := p.Question()
	if err != nil {
		t.Errorf("question: %v", err)
		return nil
	}
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: h.ID, Response: true, Authoritative: true})
	b.EnableCompression()
	b.StartQuestions()
	b.Question(question)
	b.StartAnswers()
	if question.Type == dnsmessage.TypeA {
		b.AResource(dnsmessage.ResourceHeader{Name: question.Name, Class: dnsmessage.ClassINET, TTL: 60}, dnsmessage.AResource{A: [4]byte{192, 0, 2, 7}})
	}
	out, _ := b.Finish()
	return out
}

// udpServer answers DNS over UDP until the test ends, and returns its address.
func udpServer(t *testing.T) string {
	c, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	go func() {
		buf := make([]byte, 512)
		for {
			n, from, err := c.ReadFrom(buf)
			if err != nil {
				return
			}
			c.WriteTo(answer(t, buf[:n]), from)
		}
	}()
	return c.LocalAddr().String()
}

func lookup(t *testing.T, r *net.Resolver) ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return r.LookupHost(ctx, "example.test")
}

// A first server that is down leaves the lookup to the next one.
func TestResolverMovesPastDeadServer(t *testing.T) {
	dead, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	deadAddr := dead.LocalAddr().String()
	dead.Close()
	got, err := lookup(t, resolver([]string{deadAddr, udpServer(t)}, "", nil))
	if err != nil || len(got) != 1 || got[0] != "192.0.2.7" {
		t.Fatalf("got %v, %v", got, err)
	}
}

// A first server answering that it failed, a router whose own upstream is
// down, leaves the lookup to the next one.
func TestResolverMovesPastFailingServer(t *testing.T) {
	c, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	go func() {
		buf := make([]byte, 512)
		for {
			n, from, err := c.ReadFrom(buf)
			if err != nil {
				return
			}
			reply := append([]byte{}, buf[:n]...)
			reply[2] |= 0x80
			reply[3] = reply[3]&0xF0 | 2
			c.WriteTo(reply, from)
		}
	}()
	got, err := lookup(t, resolver([]string{c.LocalAddr().String(), udpServer(t)}, "", nil))
	if err != nil || len(got) != 1 || got[0] != "192.0.2.7" {
		t.Fatalf("got %v, %v", got, err)
	}
}

// With a Private DNS host, lookups go over TLS to it and check its name.
func TestResolverOverTLS(t *testing.T) {
	srv := httptest.NewUnstartedServer(nil)
	srv.StartTLS()
	cert := srv.TLS.Certificates[0]
	srv.Close()
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				for {
					var n uint16
					if binary.Read(c, binary.BigEndian, &n) != nil {
						return
					}
					q := make([]byte, n)
					if _, err := io.ReadFull(c, q); err != nil {
						return
					}
					a := answer(t, q)
					binary.Write(c, binary.BigEndian, uint16(len(a)))
					c.Write(a)
				}
			}()
		}
	}()
	roots := x509.NewCertPool()
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	roots.AddCert(leaf)
	// httptest's certificate names example.com.
	got, err := lookup(t, resolver([]string{ln.Addr().String()}, "example.com", roots))
	if err != nil || len(got) != 1 || got[0] != "192.0.2.7" {
		t.Fatalf("over TLS: got %v, %v", got, err)
	}
	if _, err := lookup(t, resolver([]string{ln.Addr().String()}, "dns.wrong.test", roots)); err == nil {
		t.Fatal("a certificate naming another host was accepted")
	}
	// The Private DNS host's first address down leaves the lookup to the next.
	closed, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	dead := closed.Addr().String()
	closed.Close()
	got, err = lookup(t, resolver([]string{dead, ln.Addr().String()}, "example.com", roots))
	if err != nil || len(got) != 1 || got[0] != "192.0.2.7" {
		t.Fatalf("past a dead TLS server: got %v, %v", got, err)
	}
}
