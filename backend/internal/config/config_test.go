package config

import (
	"strings"
	"testing"
)

const tok = "123456789:AAbbccddeeffgghhiijjkkllmmnnooppqq"

func TestTelegramAndProxies(t *testing.T) {
	t.Setenv("TELEGRAM_TOKEN", tok)
	t.Setenv("ADMIN_ID", "111, -1002233")
	t.Setenv("TELEGRAM_PROXY", "socks5h://u:p@127.0.0.1:1080")
	t.Setenv("MRSMITH_GITHUB_PROXY", "socks5://10.0.0.2:9050")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !c.Telegram.Enabled() || len(c.Telegram.Chats) != 2 || c.Telegram.Chats[1] != -1002233 {
		t.Fatalf("telegram %+v", c.Telegram)
	}
	if c.Telegram.Proxy == nil || !c.Telegram.Proxy.RemoteDNS() || c.GitHubProxy == nil || c.GitHubProxy.RemoteDNS() {
		t.Fatal("proxies not parsed")
	}
}

func TestTelegramErrors(t *testing.T) {
	for _, tc := range []struct{ token, ids, proxy, want string }{
		{"nope", "1", "", "TELEGRAM_TOKEN"},
		{tok, "", "", "ADMIN_ID"},
		{tok, "@me", "", "ADMIN_ID"},
		{tok, "1", "http://127.0.0.1:8080", "TELEGRAM_PROXY"},
	} {
		t.Setenv("TELEGRAM_TOKEN", tc.token)
		t.Setenv("ADMIN_ID", tc.ids)
		t.Setenv("TELEGRAM_PROXY", tc.proxy)
		if _, err := Load(); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%+v: got %v", tc, err)
		}
	}
	t.Setenv("TELEGRAM_TOKEN", "")
	t.Setenv("ADMIN_ID", "")
	t.Setenv("TELEGRAM_PROXY", "")
	t.Setenv("MRSMITH_GITHUB_PROXY", "socks4://x:1")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "MRSMITH_GITHUB_PROXY") {
		t.Fatalf("got %v", err)
	}
}
