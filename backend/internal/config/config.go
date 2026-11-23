package config

import (
	"fmt"
	"net/netip"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"mrsmith/internal/netproxy"
)

type Config struct {
	Listen            string
	DataDir           string
	BackupDir         string // archives live here: ./backup next to docker-compose.yml
	PanelPath         string // where the admin UI and API live, e.g. "/" or "/x7k2m/"
	PublicURL         string // optional base for generated links, e.g. https://rules.example.com
	CookieSecure      bool
	TrustedProxies    []netip.Prefix
	SessionTTL        time.Duration
	SessionIdle       time.Duration
	MaxUploadBytes    int64
	MaxFetchBytes     int64
	FetchTimeout      time.Duration
	AllowPrivateFetch bool
	FetchAllow        []netip.Prefix // extra ranges allowed despite SSRF rules, e.g. fake-ip 198.18.0.0/15
	GitHubProxy       *netproxy.Proxy
	Telegram          Telegram
	LogLevel          string
}

// Telegram sends change notices to the admins. Empty Token turns it off.
type Telegram struct {
	Token string
	Chats []int64
	Proxy *netproxy.Proxy
}

func (t Telegram) Enabled() bool { return t.Token != "" && len(t.Chats) > 0 }

var botTokenRe = regexp.MustCompile(`^[0-9]{3,20}:[A-Za-z0-9_-]{30,100}$`)

func Load() (*Config, error) {
	c := &Config{
		Listen:            env("MRSMITH_LISTEN", ":8080"),
		DataDir:           env("MRSMITH_DATA_DIR", "/data"),
		BackupDir:         env("MRSMITH_BACKUP_DIR", "/backup"),
		PanelPath:         env("MRSMITH_PANEL_PATH", "/"),
		PublicURL:         strings.TrimRight(env("MRSMITH_PUBLIC_URL", ""), "/"),
		LogLevel:          env("MRSMITH_LOG_LEVEL", "info"),
		CookieSecure:      true,
		SessionTTL:        7 * 24 * time.Hour,
		SessionIdle:       24 * time.Hour,
		MaxUploadBytes:    100 << 20,
		MaxFetchBytes:     64 << 20,
		FetchTimeout:      60 * time.Second,
		AllowPrivateFetch: false,
	}
	var err error
	if c.CookieSecure, err = envBool("MRSMITH_COOKIE_SECURE", c.CookieSecure); err != nil {
		return nil, err
	}
	if c.AllowPrivateFetch, err = envBool("MRSMITH_ALLOW_PRIVATE_FETCH", c.AllowPrivateFetch); err != nil {
		return nil, err
	}
	if c.SessionTTL, err = envDuration("MRSMITH_SESSION_TTL", c.SessionTTL); err != nil {
		return nil, err
	}
	if c.SessionIdle, err = envDuration("MRSMITH_SESSION_IDLE", c.SessionIdle); err != nil {
		return nil, err
	}
	if c.FetchTimeout, err = envDuration("MRSMITH_FETCH_TIMEOUT", c.FetchTimeout); err != nil {
		return nil, err
	}
	if c.MaxUploadBytes, err = envMB("MRSMITH_MAX_UPLOAD_MB", c.MaxUploadBytes); err != nil {
		return nil, err
	}
	if c.MaxFetchBytes, err = envMB("MRSMITH_MAX_FETCH_MB", c.MaxFetchBytes); err != nil {
		return nil, err
	}

	proxies := env("MRSMITH_TRUSTED_PROXIES", "127.0.0.0/8,::1/128,10.0.0.0/8,172.16.0.0/12,192.168.0.0/16,fc00::/7")
	for _, p := range strings.Split(proxies, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		pfx, err := netip.ParsePrefix(p)
		if err != nil {
			return nil, fmt.Errorf("MRSMITH_TRUSTED_PROXIES: %q: %w", p, err)
		}
		c.TrustedProxies = append(c.TrustedProxies, pfx.Masked())
	}

	for _, p := range strings.Split(env("MRSMITH_FETCH_ALLOW_CIDRS", ""), ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		pfx, err := netip.ParsePrefix(p)
		if err != nil {
			return nil, fmt.Errorf("MRSMITH_FETCH_ALLOW_CIDRS: %q: %w", p, err)
		}
		c.FetchAllow = append(c.FetchAllow, pfx.Masked())
	}

	if v := env("MRSMITH_GITHUB_PROXY", ""); v != "" {
		if c.GitHubProxy, err = netproxy.Parse(v); err != nil {
			return nil, fmt.Errorf("MRSMITH_GITHUB_PROXY: %w", err)
		}
	}
	if c.Telegram, err = loadTelegram(); err != nil {
		return nil, err
	}

	c.PanelPath = "/" + strings.Trim(c.PanelPath, "/")
	if c.PanelPath != "/" {
		c.PanelPath += "/"
	}
	for _, r := range c.PanelPath {
		if !(r == '/' || r == '-' || r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return nil, fmt.Errorf("MRSMITH_PANEL_PATH может содержать только латиницу, цифры, - и _")
		}
	}
	if strings.HasPrefix(c.PanelPath, "/r/") || strings.HasPrefix(c.PanelPath, "/f/") || c.PanelPath == "/healthz/" {
		return nil, fmt.Errorf("MRSMITH_PANEL_PATH не может начинаться с /r/, /f/ или быть /healthz")
	}
	if c.SessionIdle > c.SessionTTL {
		c.SessionIdle = c.SessionTTL
	}
	return c, nil
}

func loadTelegram() (Telegram, error) {
	var t Telegram
	t.Token = env("TELEGRAM_TOKEN", "")
	ids := env("ADMIN_ID", "")
	if t.Token == "" {
		return t, nil
	}
	if !botTokenRe.MatchString(t.Token) {
		return t, fmt.Errorf("TELEGRAM_TOKEN: ожидается токен от @BotFather вида 123456789:AA…")
	}
	for _, s := range strings.Split(ids, ",") {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		id, err := strconv.ParseInt(s, 10, 64)
		if err != nil || id == 0 {
			return t, fmt.Errorf("ADMIN_ID: %q не похоже на числовой Telegram ID", s)
		}
		t.Chats = append(t.Chats, id)
	}
	if len(t.Chats) == 0 {
		return t, fmt.Errorf("ADMIN_ID: укажите ваш числовой Telegram ID (узнать его можно у @userinfobot), несколько — через запятую")
	}
	if len(t.Chats) > 20 {
		return t, fmt.Errorf("ADMIN_ID: не больше 20 получателей")
	}
	if v := env("TELEGRAM_PROXY", ""); v != "" {
		p, err := netproxy.Parse(v)
		if err != nil {
			return t, fmt.Errorf("TELEGRAM_PROXY: %w", err)
		}
		t.Proxy = p
	}
	return t, nil
}

func env(k, def string) string {
	if v, ok := os.LookupEnv(k); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return def
}

func envBool(k string, def bool) (bool, error) {
	v := env(k, "")
	if v == "" {
		return def, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def, fmt.Errorf("%s: ожидается true/false", k)
	}
	return b, nil
}

func envDuration(k string, def time.Duration) (time.Duration, error) {
	v := env(k, "")
	if v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return def, fmt.Errorf("%s: ожидается длительность вроде 24h или 30m", k)
	}
	return d, nil
}

func envMB(k string, def int64) (int64, error) {
	v := env(k, "")
	if v == "" {
		return def, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n <= 0 || n > 10240 {
		return def, fmt.Errorf("%s: ожидается число мегабайт от 1 до 10240", k)
	}
	return n << 20, nil
}
