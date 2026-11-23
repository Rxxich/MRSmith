package fetch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"mrsmith/internal/netproxy"
	"mrsmith/internal/netproxy/socks5test"
)

func TestBlocksPrivateAddresses(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("secret")) }))
	defer srv.Close()
	c := New(5*time.Second, 1<<20, false, nil, "test")
	for _, u := range []string{srv.URL, "http://localhost:" + strings.Split(srv.URL, ":")[2], "http://[::1]/", "http://169.254.169.254/latest/meta-data/"} {
		if _, err := c.Get(context.Background(), u, "", ""); err == nil {
			t.Errorf("%s: fetched an internal address", u)
		}
	}
	for _, u := range []string{"file:///etc/passwd", "gopher://x", "ftp://example.com/a"} {
		if _, err := c.Get(context.Background(), u, "", ""); err == nil {
			t.Errorf("%s: scheme accepted", u)
		}
	}
}

func TestRedirectToPrivateBlocked(t *testing.T) {
	// The allow-list check happens at dial time, so a public host redirecting
	// to an internal one is still refused. Simulate with allowPrivate=false
	// against a local server: the very first dial already fails.
	srv := httptest.NewServer(http.RedirectHandler("http://127.0.0.1:1/", http.StatusFound))
	defer srv.Close()
	c := New(5*time.Second, 1<<20, false, nil, "test")
	if _, err := c.Get(context.Background(), srv.URL, "", ""); err == nil {
		t.Fatal("redirect chain reached an internal address")
	}
}

func TestSizeLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(make([]byte, 2048))
	}))
	defer srv.Close()
	c := New(5*time.Second, 1024, true, nil, "test")
	if _, err := c.Get(context.Background(), srv.URL, "", ""); err != ErrTooLarge {
		t.Fatalf("err = %v", err)
	}
}

func TestIsPublicIP(t *testing.T) {
	for s, want := range map[string]bool{
		"8.8.8.8": true, "1.1.1.1": true, "2606:4700::1111": true,
		"10.1.2.3": false, "192.168.0.1": false, "127.0.0.1": false, "100.64.0.1": false,
		"169.254.169.254": false, "::1": false, "fd00::1": false, "::ffff:127.0.0.1": false, "0.0.0.0": false,
	} {
		if got := IsPublicIP(netip.MustParseAddr(s)); got != want {
			t.Errorf("IsPublicIP(%s) = %v", s, got)
		}
	}
}

func TestAllowList(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) }))
	defer srv.Close()
	c := New(5*time.Second, 1<<20, false, []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}, "test")
	if _, err := c.Get(context.Background(), srv.URL, "", ""); err != nil {
		t.Fatalf("allow-listed range refused: %v", err)
	}
}

func TestGitHubGoesThroughProxy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("rules for " + r.Host))
	}))
	defer srv.Close()
	local := strings.TrimPrefix(srv.URL, "http://")
	socks := socks5test.Start(t, "", "", func(string) string { return local })
	p, err := netproxy.Parse("socks5h://" + socks.Addr)
	if err != nil {
		t.Fatal(err)
	}
	c := New(5*time.Second, 1<<20, true, nil, "test", WithGitHubProxy(p))

	res, err := c.Get(context.Background(), "http://raw.githubusercontent.com/a/b/main/list.txt", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if string(res.Body) != "rules for raw.githubusercontent.com" {
		t.Fatalf("body %q", res.Body)
	}
	if tg := socks.Targets(); len(tg) != 1 || tg[0] != "raw.githubusercontent.com:80" {
		t.Fatalf("proxy saw %v", tg)
	}

	// Other hosts are fetched directly.
	if _, err := c.Get(context.Background(), srv.URL, "", ""); err != nil {
		t.Fatal(err)
	}
	if n := len(socks.Targets()); n != 1 {
		t.Fatalf("a non-GitHub request went through the proxy (%d connects)", n)
	}
}

func TestIsGitHubHost(t *testing.T) {
	for h, want := range map[string]bool{
		"github.com": true, "raw.githubusercontent.com": true, "objects.githubusercontent.com": true,
		"codeload.github.com": true, "user.github.io": true, "GitHub.com.": true,
		"notgithub.com": false, "github.com.evil.io": false, "cdn.jsdelivr.net": false, "githubusercontent.com.ru": false,
	} {
		if got := IsGitHubHost(h); got != want {
			t.Errorf("%s: %v, want %v", h, got, want)
		}
	}
}

func TestSilentServerTimesOutInPlainWords(t *testing.T) {
	headers := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer headers.Close()
	drip := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for {
			w.Write([]byte("a.example\n"))
			w.(http.Flusher).Flush()
			select {
			case <-r.Context().Done():
				return
			case <-time.After(50 * time.Millisecond):
			}
		}
	}))
	defer drip.Close()
	c := New(400*time.Millisecond, 1<<20, true, nil, "test")
	for _, u := range []string{headers.URL, drip.URL} {
		start := time.Now()
		_, err := c.Get(context.Background(), u, "", "")
		if err == nil || err.Error() != "сервер не ответил вовремя" {
			t.Fatalf("%s: %v", u, err)
		}
		if d := time.Since(start); d > 3*time.Second {
			t.Fatalf("%s: gave up after %v", u, d)
		}
	}
}

func TestHangUpInPlainWords(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			conn.Close()
		}
	}))
	defer srv.Close()
	c := New(2*time.Second, 1<<20, true, nil, "test")
	if _, err := c.Get(context.Background(), srv.URL, "", ""); err == nil || !strings.Contains(err.Error(), "закрыл соединение") {
		t.Fatalf("got %v", err)
	}
}
