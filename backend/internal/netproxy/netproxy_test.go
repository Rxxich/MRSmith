package netproxy

import (
	"context"
	"errors"
	"io"
	"net"
	"net/netip"
	"strings"
	"testing"

	"mrsmith/internal/netproxy/socks5test"
)

func TestParse(t *testing.T) {
	for _, bad := range []string{"", "http://127.0.0.1:1080", "socks5://127.0.0.1", "socks5://:1080", "socks5h://h:1/path", "socks4://h:1"} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	p, err := Parse("socks5h://user:secret@127.0.0.1:1080")
	if err != nil {
		t.Fatal(err)
	}
	if !p.RemoteDNS() || !p.HasAuth() || strings.Contains(p.String(), "secret") || p.String() != "socks5h://127.0.0.1:1080" {
		t.Fatalf("parsed %v remote=%v auth=%v", p, p.RemoteDNS(), p.HasAuth())
	}
	if p, _ := Parse("SOCKS5://10.0.0.1:9050"); p == nil || p.RemoteDNS() {
		t.Fatal("socks5 should resolve locally")
	}
}

// echo answers every connection with "hi".
func echo(t *testing.T) string {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
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
			c.Write([]byte("hi"))
			c.Close()
		}
	}()
	return ln.Addr().String()
}

func read(t *testing.T, c net.Conn) string {
	t.Helper()
	defer c.Close()
	b, _ := io.ReadAll(c)
	return string(b)
}

func TestRemoteDNSSendsName(t *testing.T) {
	dst := echo(t)
	srv := socks5test.Start(t, "u", "p", func(string) string { return dst })
	p, err := Parse("socks5h://u:p@" + srv.Addr)
	if err != nil {
		t.Fatal(err)
	}
	c, err := p.DialContext(context.Background(), "tcp", "raw.githubusercontent.com:443", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := read(t, c); got != "hi" {
		t.Fatalf("read %q", got)
	}
	if tg := srv.Targets(); len(tg) != 1 || tg[0] != "raw.githubusercontent.com:443" {
		t.Fatalf("proxy saw %v, want the host name", tg)
	}
}

func TestLocalDNSSendsCheckedIP(t *testing.T) {
	dst := echo(t)
	srv := socks5test.Start(t, "", "", func(string) string { return dst })
	p, _ := Parse("socks5://" + srv.Addr)
	c, err := p.DialContext(context.Background(), "tcp", "localhost:443", func(netip.Addr) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	read(t, c)
	tg := srv.Targets()
	if len(tg) != 1 {
		t.Fatalf("targets %v", tg)
	}
	host, _, _ := net.SplitHostPort(tg[0])
	if _, err := netip.ParseAddr(host); err != nil {
		t.Fatalf("proxy got %q, want an IP resolved locally", tg[0])
	}
	deny := errors.New("internal")
	if _, err := p.DialContext(context.Background(), "tcp", "localhost:443", func(netip.Addr) error { return deny }); !errors.Is(err, deny) {
		t.Fatalf("check was not applied: %v", err)
	}
	if n := len(srv.Targets()); n != 1 {
		t.Fatalf("refused address still reached the proxy (%d connects)", n)
	}
}

func TestWrongPassword(t *testing.T) {
	srv := socks5test.Start(t, "u", "p", nil)
	p, _ := Parse("socks5h://u:nope@" + srv.Addr)
	_, err := p.DialContext(context.Background(), "tcp", "github.com:443", nil)
	var perr *Error
	if !errors.As(err, &perr) || strings.Contains(err.Error(), "nope") {
		t.Fatalf("got %v", err)
	}
}
