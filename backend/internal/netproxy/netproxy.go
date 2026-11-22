// Package netproxy opens TCP connections through a SOCKS5 proxy.
//
// socks5://  — the host name is resolved here and the proxy gets an IP.
// socks5h:// — the host name goes to the proxy, which resolves it itself.
package netproxy

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"sort"
	"strings"
	"time"

	"golang.org/x/net/proxy"
)

type Proxy struct {
	scheme string
	addr   string
	user   bool
	dialer proxy.ContextDialer
}

// Error marks failures that happened on the way through the proxy.
type Error struct {
	Proxy string
	Err   error
}

func (e *Error) Error() string { return fmt.Sprintf("прокси %s: %v", e.Proxy, e.Err) }
func (e *Error) Unwrap() error { return e.Err }

// Parse reads socks5://[user:password@]host:port or socks5h://…
func Parse(raw string) (*Proxy, error) {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil {
		return nil, errors.New("некорректный адрес прокси")
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "socks5" && scheme != "socks5h" {
		return nil, errors.New("поддерживаются только socks5:// и socks5h://")
	}
	if u.Hostname() == "" || u.Port() == "" {
		return nil, errors.New("укажите адрес и порт прокси, например socks5h://127.0.0.1:1080")
	}
	if (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("в адресе прокси не должно быть пути и параметров")
	}
	var auth *proxy.Auth
	if u.User != nil {
		pw, _ := u.User.Password()
		name := u.User.Username()
		if name == "" || len(name) > 255 || len(pw) > 255 {
			return nil, errors.New("логин и пароль прокси: от 1 до 255 байт")
		}
		auth = &proxy.Auth{User: name, Password: pw}
	}
	forward := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	d, err := proxy.SOCKS5("tcp", u.Host, auth, forward)
	if err != nil {
		return nil, err
	}
	cd, ok := d.(proxy.ContextDialer)
	if !ok {
		return nil, errors.New("SOCKS5-клиент не поддерживает отмену соединений")
	}
	return &Proxy{scheme: scheme, addr: u.Host, user: auth != nil, dialer: cd}, nil
}

// String shows the proxy without its credentials.
func (p *Proxy) String() string { return p.scheme + "://" + p.addr }

// RemoteDNS reports whether the proxy resolves host names (socks5h).
func (p *Proxy) RemoteDNS() bool { return p.scheme == "socks5h" }

// HasAuth reports whether a login and password are set.
func (p *Proxy) HasAuth() bool { return p.user }

// DialContext connects to address through the proxy. With socks5 the host is
// resolved locally and every address must pass check (when set) before it is
// handed to the proxy; with socks5h the proxy resolves the name itself.
func (p *Proxy) DialContext(ctx context.Context, network, address string, check func(netip.Addr) error) (net.Conn, error) {
	if network != "tcp" && network != "tcp4" && network != "tcp6" {
		return nil, &Error{p.String(), fmt.Errorf("сеть %s не поддерживается", network)}
	}
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	if p.RemoteDNS() {
		if ip, err := netip.ParseAddr(host); err == nil && check != nil {
			if err := check(ip); err != nil {
				return nil, err
			}
		}
		c, err := p.dialer.DialContext(ctx, "tcp", address)
		if err != nil {
			return nil, &Error{p.String(), err}
		}
		return c, nil
	}
	var addrs []netip.Addr
	if ip, err := netip.ParseAddr(host); err == nil {
		addrs = []netip.Addr{ip}
	} else {
		addrs, err = net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		if err != nil {
			return nil, err
		}
	}
	// IPv4 first: many proxies have no IPv6 uplink.
	sort.SliceStable(addrs, func(i, j int) bool { return addrs[i].Unmap().Is4() && !addrs[j].Unmap().Is4() })
	var last error
	for _, ip := range addrs {
		ip = ip.Unmap()
		if check != nil {
			if err := check(ip); err != nil {
				last = err
				continue
			}
		}
		c, err := p.dialer.DialContext(ctx, "tcp", net.JoinHostPort(ip.String(), port))
		if err == nil {
			return c, nil
		}
		last = &Error{p.String(), err}
		if ctx.Err() != nil {
			break
		}
	}
	if last == nil {
		last = fmt.Errorf("домен %s не найден (DNS)", host)
	}
	return nil, last
}
