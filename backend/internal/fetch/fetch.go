// Package fetch downloads rule sources over HTTP(S) with SSRF protection.
package fetch

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"syscall"
	"time"

	"mrsmith/internal/netproxy"
)

type Client struct {
	http         *http.Client
	maxBytes     int64
	allowPrivate bool
	allow        []netip.Prefix
	userAgent    string
	github       *netproxy.Proxy
}

// Option tunes a Client.
type Option func(*Client)

// WithGitHubProxy sends requests to GitHub hosts through a SOCKS5 proxy.
// Other hosts are fetched directly.
func WithGitHubProxy(p *netproxy.Proxy) Option { return func(c *Client) { c.github = p } }

type Result struct {
	Body         []byte
	ETag         string
	LastModified string
	NotModified  bool
}

var ErrTooLarge = errors.New("файл по ссылке больше допустимого размера")

// fakeIP is the range mihomo/sing-box hand out in TUN fake-ip mode.
var fakeIP = netip.MustParsePrefix("198.18.0.0/15")

func New(timeout time.Duration, maxBytes int64, allowPrivate bool, allow []netip.Prefix, version string, opts ...Option) *Client {
	c := &Client{maxBytes: maxBytes, allowPrivate: allowPrivate, allow: allow, userAgent: "MRSmith/" + version}
	for _, o := range opts {
		o(c)
	}
	dialer := &net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 30 * time.Second,
		// Control runs with the address actually being dialled, after DNS
		// resolution, so rebinding tricks cannot reach internal hosts.
		Control: func(network, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			ip, err := netip.ParseAddr(host)
			if err != nil {
				return err
			}
			return c.checkIP(ip)
		},
	}
	dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		if c.github != nil {
			if host, _, err := net.SplitHostPort(addr); err == nil && IsGitHubHost(host) {
				// With socks5 the address is resolved here and checked like
				// a direct one; with socks5h the proxy resolves a GitHub name.
				return c.github.DialContext(ctx, network, addr, c.checkIP)
			}
		}
		return dialer.DialContext(ctx, network, addr)
	}
	transport := &http.Transport{
		Proxy:                  nil, // never pick up proxy variables implicitly
		DialContext:            dial,
		ForceAttemptHTTP2:      true,
		MaxIdleConns:           20,
		IdleConnTimeout:        60 * time.Second,
		TLSHandshakeTimeout:    10 * time.Second,
		ResponseHeaderTimeout:  30 * time.Second,
		ExpectContinueTimeout:  time.Second,
		MaxResponseHeaderBytes: 1 << 20,
		TLSClientConfig:        &tls.Config{MinVersion: tls.VersionTLS12},
	}
	c.http = &http.Client{
		Transport: transport,
		Timeout:   timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("слишком много перенаправлений")
			}
			return ValidateURL(req.URL.String())
		},
	}
	return c
}

// checkIP refuses internal addresses unless they are explicitly allowed.
func (c *Client) checkIP(ip netip.Addr) error {
	if c.allowPrivate || IsPublicIP(ip) || c.allowed(ip) {
		return nil
	}
	if fakeIP.Contains(ip.Unmap()) {
		return fmt.Errorf("домен указывает на %s — это fake-ip от mihomo/sing-box в режиме TUN на этом сервере; "+
			"разрешите диапазон переменной MRSMITH_FETCH_ALLOW_CIDRS=198.18.0.0/15", ip)
	}
	return fmt.Errorf("адрес %s находится во внутренней сети — загрузка запрещена", ip)
}

// GitHubProxy returns the proxy used for GitHub, or nil.
func (c *Client) GitHubProxy() *netproxy.Proxy { return c.github }

// githubDomains are the hosts GitHub serves repositories, raw files,
// releases and Pages from. Subdomains match too.
var githubDomains = []string{"github.com", "githubusercontent.com", "github.io", "githubassets.com"}

// IsGitHubHost reports whether host belongs to GitHub.
func IsGitHubHost(host string) bool {
	h := strings.ToLower(strings.TrimSuffix(strings.Trim(host, "[]"), "."))
	for _, d := range githubDomains {
		if h == d || strings.HasSuffix(h, "."+d) {
			return true
		}
	}
	return false
}

func (c *Client) allowed(ip netip.Addr) bool {
	ip = ip.Unmap()
	for _, p := range c.allow {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// ValidateURL accepts only absolute http(s) URLs.
func ValidateURL(raw string) error {
	if len(raw) > 2048 {
		return errors.New("ссылка слишком длинная")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return errors.New("некорректная ссылка")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return errors.New("поддерживаются только ссылки http:// и https://")
	}
	if u.Hostname() == "" {
		return errors.New("в ссылке нет адреса сервера")
	}
	return nil
}

// RedactURL hides credentials and query strings, which often carry tokens.
func RedactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "(invalid url)"
	}
	u.User = nil
	if u.RawQuery != "" {
		u.RawQuery = "…"
	}
	return u.String()
}

// Get downloads url. With etag/lastModified it performs a conditional request.
func (c *Client) Get(ctx context.Context, rawURL, etag, lastModified string) (*Result, error) {
	if err := ValidateURL(rawURL); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Accept", "*/*")
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	if lastModified != "" {
		req.Header.Set("If-Modified-Since", lastModified)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, friendly(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotModified {
		return &Result{NotModified: true, ETag: etag, LastModified: lastModified}, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("сервер ответил %d %s", resp.StatusCode, http.StatusText(resp.StatusCode))
	}
	if resp.ContentLength > c.maxBytes {
		return nil, ErrTooLarge
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, c.maxBytes+1))
	if err != nil {
		return nil, friendly(err)
	}
	if int64(len(body)) > c.maxBytes {
		return nil, ErrTooLarge
	}
	return &Result{Body: body, ETag: resp.Header.Get("ETag"), LastModified: resp.Header.Get("Last-Modified")}, nil
}

func friendly(err error) error {
	var uerr *url.Error
	if errors.As(err, &uerr) {
		err = uerr.Err
	}
	var perr *netproxy.Error
	if errors.As(err, &perr) {
		return fmt.Errorf("не удалось подключиться через прокси для GitHub %s: %v", perr.Proxy, perr.Err)
	}
	msg := err.Error()
	var nerr net.Error
	switch {
	case errors.Is(err, context.Canceled):
		return err
	case errors.Is(err, context.DeadlineExceeded) || strings.Contains(msg, "Client.Timeout") ||
		strings.Contains(msg, "timeout awaiting response headers") || errors.As(err, &nerr) && nerr.Timeout():
		return errors.New("сервер не ответил вовремя")
	case strings.Contains(msg, "no such host"):
		return errors.New("домен не найден (DNS)")
	case errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || strings.Contains(msg, "connection reset"):
		return errors.New("сервер закрыл соединение, не отдав ответ целиком")
	case strings.Contains(msg, "connection refused") || strings.Contains(msg, "actively refused"):
		return errors.New("сервер отказал в соединении")
	case strings.Contains(msg, "certificate"):
		return errors.New("ошибка TLS-сертификата сервера")
	}
	return err
}

var blocked = func() []netip.Prefix {
	var out []netip.Prefix
	for _, s := range []string{
		"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16",
		"172.16.0.0/12", "192.0.0.0/24", "192.0.2.0/24", "192.88.99.0/24", "192.168.0.0/16",
		"198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "224.0.0.0/3",
		"::/128", "::1/128", "64:ff9b:1::/48", "100::/64", "2001:db8::/32", "fc00::/7", "fe80::/10", "ff00::/8",
	} {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}()

// IsPublicIP reports whether ip is routable on the public internet.
func IsPublicIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() || ip.IsInterfaceLocalMulticast() {
		return false
	}
	for _, p := range blocked {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}
