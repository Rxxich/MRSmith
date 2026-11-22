package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"mrsmith/internal/audit"
	"mrsmith/internal/auth"
	"mrsmith/internal/config"
	"mrsmith/internal/downloads"
	"mrsmith/internal/fetch"
	"mrsmith/internal/rules"
	"mrsmith/internal/rulesets"
	"mrsmith/internal/storage"
	"mrsmith/internal/store"
)

type env struct {
	t      *testing.T
	srv    *httptest.Server
	client *http.Client
	csrf   string
	mgr    *rulesets.Manager
	cfg    *config.Config
	user   string
	pass   string
}

func (e *env) creds() map[string]string {
	return map[string]string{"username": e.user, "password": e.pass}
}

func (e *env) srvCfg() *config.Config { return e.cfg }

func newEnv(t *testing.T, panel string) *env {
	t.Helper()
	dir := t.TempDir()
	cfg := &config.Config{
		DataDir: dir, PanelPath: panel, CookieSecure: false,
		SessionTTL: time.Hour, SessionIdle: time.Hour,
		MaxUploadBytes: 5 << 20, MaxFetchBytes: 5 << 20,
		TrustedProxies: []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")},
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	db, err := store.Open(dir + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	a := auth.New(db, cfg.SessionTTL, cfg.SessionIdle)
	user, pass, err := a.Bootstrap(io.Discard)
	if err != nil || !strings.HasPrefix(user, "smith-") || len(pass) < 20 {
		t.Fatalf("bootstrap: %q %v", user, err)
	}
	if again, _, _ := a.Bootstrap(io.Discard); again != "" {
		t.Fatal("bootstrap created a second admin")
	}
	st, _ := storage.New(db, dir, cfg.MaxUploadBytes)
	f := fetch.New(5*time.Second, cfg.MaxFetchBytes, true, nil, "test")
	mgr, _ := rulesets.NewManager(db, st, f, dir, cfg.MaxFetchBytes, log)
	ui := fstest.MapFS{"index.html": {Data: []byte(`<html><head><base href="/"></head></html>`)}, "assets/app.js": {Data: []byte("x")}}
	s, err := New(Deps{Config: cfg, Auth: a, Rulesets: mgr, Storage: st, Fetch: f, Audit: audit.New(mgr, st, cfg.MaxFetchBytes),
		Downloads: downloads.New(db, mgr, st, log), Log: log, Version: "test", UI: ui, Ctx: context.Background()})
	if err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewServer(s)
	t.Cleanup(hs.Close)
	jar, _ := cookiejar.New(nil)
	return &env{t: t, srv: hs, client: &http.Client{Jar: jar}, mgr: mgr, cfg: cfg, user: user, pass: pass}
}

func (e *env) do(method, path string, body any, headers map[string]string) (*http.Response, []byte) {
	e.t.Helper()
	var rd io.Reader
	ct := ""
	switch b := body.(type) {
	case nil:
	case *bytes.Buffer:
		rd = b
	default:
		raw, _ := json.Marshal(b)
		rd = bytes.NewReader(raw)
		ct = "application/json"
	}
	req, _ := http.NewRequest(method, e.srv.URL+path, rd)
	if ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	req.Header.Set("X-Requested-With", "MRSmith")
	if e.csrf != "" {
		req.Header.Set("X-CSRF-Token", e.csrf)
	}
	for k, v := range headers {
		if k == "Host" {
			req.Host = v
			continue
		}
		req.Header.Set(k, v)
	}
	resp, err := e.client.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp, out
}

func (e *env) login(panel string) {
	e.t.Helper()
	resp, body := e.do("POST", panel+"api/auth/login", e.creds(), nil)
	if resp.StatusCode != 200 {
		e.t.Fatalf("login: %d %s", resp.StatusCode, body)
	}
	var me struct{ CSRF string }
	json.Unmarshal(body, &me)
	e.csrf = me.CSRF
}

func multipartBody(fields map[string]string, fileField, fileName string, content []byte) (*bytes.Buffer, string) {
	buf := &bytes.Buffer{}
	mw := multipart.NewWriter(buf)
	for k, v := range fields {
		mw.WriteField(k, v)
	}
	if fileField != "" {
		fw, _ := mw.CreateFormFile(fileField, fileName)
		fw.Write(content)
	}
	mw.Close()
	return buf, mw.FormDataContentType()
}

func TestAuthFlow(t *testing.T) {
	e := newEnv(t, "/")
	if resp, _ := e.do("GET", "/api/rulesets", nil, nil); resp.StatusCode != 401 {
		t.Fatalf("unauthenticated = %d", resp.StatusCode)
	}
	resp, _ := e.do("POST", "/api/auth/login", map[string]string{"username": e.user, "password": "wrong-password"}, nil)
	if resp.StatusCode != 401 {
		t.Fatalf("bad password = %d", resp.StatusCode)
	}
	// login without the custom header is refused (CSRF on login)
	req, _ := http.NewRequest("POST", e.srv.URL+"/api/auth/login", strings.NewReader(`{"username":"`+e.user+`","password":"`+e.pass+`"}`))
	req.Header.Set("Content-Type", "application/json")
	if r, _ := e.client.Do(req); r.StatusCode != 403 {
		t.Fatalf("login without header = %d", r.StatusCode)
	}
	e.login("/")
	if resp, _ := e.do("GET", "/api/auth/me", nil, nil); resp.StatusCode != 200 {
		t.Fatalf("me = %d", resp.StatusCode)
	}
	// unsafe request without CSRF token
	saved := e.csrf
	e.csrf = ""
	if resp, _ := e.do("POST", "/api/buckets", map[string]any{"name": "abc"}, nil); resp.StatusCode != 403 {
		t.Fatalf("no csrf = %d", resp.StatusCode)
	}
	e.csrf = saved
	// cross-site request with a valid token is still refused
	if resp, _ := e.do("POST", "/api/buckets", map[string]any{"name": "abc"}, map[string]string{"Sec-Fetch-Site": "cross-site"}); resp.StatusCode != 403 {
		t.Fatalf("cross-site = %d", resp.StatusCode)
	}
	// brute force gets blocked
	for i := 0; i < 6; i++ {
		resp, _ = e.do("POST", "/api/auth/login", map[string]string{"username": e.user, "password": "nope-nope-nope"}, nil)
	}
	if resp.StatusCode != 429 {
		t.Fatalf("brute force = %d", resp.StatusCode)
	}
	if resp, _ := e.do("POST", "/api/auth/logout", nil, nil); resp.StatusCode != 204 {
		t.Fatalf("logout = %d", resp.StatusCode)
	}
	if resp, _ := e.do("GET", "/api/auth/me", nil, nil); resp.StatusCode != 401 {
		t.Fatalf("after logout = %d", resp.StatusCode)
	}
}

func TestRulesetLifecycle(t *testing.T) {
	var version atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/a.txt":
			etag := `"v` + string(rune('0'+version.Load())) + `"`
			if r.Header.Get("If-None-Match") == etag {
				w.WriteHeader(http.StatusNotModified)
				return
			}
			w.Header().Set("ETag", etag)
			io.WriteString(w, "1.0.0.0/24\n8.8.8.8\n")
			if version.Load() > 0 {
				io.WriteString(w, "9.9.9.0/24\n")
			}
		case "/b.mrs":
			data, _ := rules.EncodeIPMRS([]netip.Prefix{netip.MustParsePrefix("1.0.1.0/24"), netip.MustParsePrefix("8.8.8.8/32")})
			w.Write(data)
		}
	}))
	defer upstream.Close()

	e := newEnv(t, "/secret-panel/")
	if resp, _ := e.do("GET", "/api/rulesets", nil, nil); resp.StatusCode != 404 {
		t.Fatalf("api outside panel path = %d", resp.StatusCode)
	}
	e.login("/secret-panel/")
	p := "/secret-panel/api"

	in := map[string]any{
		"name": "Мои IP", "behavior": "auto", "formats": []string{"mrs", "txt", "list", "yaml"},
		"aggregateIP": true, "intervalMin": 30,
		"sources": []map[string]any{
			{"type": "url", "url": upstream.URL + "/a.txt"},
			{"type": "url", "url": upstream.URL + "/b.mrs"},
		},
	}
	resp, body := e.do("POST", p+"/rulesets", in, nil)
	if resp.StatusCode != 201 {
		t.Fatalf("create: %d %s", resp.StatusCode, body)
	}
	var rs struct {
		ID, Slug string
		Sources  []struct{ ID string }
		State    struct {
			Status   string
			Count    int
			Behavior string
		}
	}
	json.Unmarshal(body, &rs)
	if rs.Slug != "moi-ip" {
		t.Errorf("slug = %s", rs.Slug)
	}
	// wait for the background build
	for i := 0; i < 100; i++ {
		_, body = e.do("GET", p+"/rulesets/"+rs.ID, nil, nil)
		json.Unmarshal(body, &rs)
		if rs.State.Status != "pending" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if rs.State.Status != "ok" || rs.State.Behavior != "ipcidr" {
		t.Fatalf("build: %s", body)
	}
	// 1.0.0.0/24 + 1.0.1.0/24 merge into /23; 8.8.8.8 is deduplicated
	resp, txt := e.do("GET", "/r/moi-ip.txt", nil, nil)
	if resp.StatusCode != 200 || string(txt) != "1.0.0.0/23\n8.8.8.8/32\n" {
		t.Fatalf("txt = %d %q", resp.StatusCode, txt)
	}
	resp, mrs := e.do("GET", "/r/moi-ip.mrs", nil, nil)
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "application/octet-stream" {
		t.Fatalf("mrs = %d", resp.StatusCode)
	}
	if m, err := rules.DecodeMRS(mrs); err != nil || len(m.Prefixes) != 2 {
		t.Fatalf("decode served mrs: %v", err)
	}
	_, list := e.do("GET", "/r/moi-ip.list", nil, nil)
	if !strings.Contains(string(list), "IP-CIDR,1.0.0.0/23") {
		t.Fatalf("list = %s", list)
	}
	// conditional GET from clients
	etag := resp.Header.Get("ETag")
	if r, _ := e.do("GET", "/r/moi-ip.mrs", nil, map[string]string{"If-None-Match": etag}); r.StatusCode != 304 {
		t.Fatalf("conditional = %d", r.StatusCode)
	}

	// upstream changes; a forced refresh picks it up
	version.Store(1)
	resp, body = e.do("POST", p+"/rulesets/"+rs.ID+"/refresh", nil, nil)
	var refreshed struct {
		Event struct {
			Kind        string
			Added       int
			SampleAdded []string
		}
	}
	json.Unmarshal(body, &refreshed)
	if resp.StatusCode != 200 || refreshed.Event.Kind != "changed" || refreshed.Event.Added != 1 || refreshed.Event.SampleAdded[0] != "9.9.9.0/24" {
		t.Fatalf("refresh: %d %s", resp.StatusCode, body)
	}
	// unchanged upstream answers 304
	_, body = e.do("POST", p+"/rulesets/"+rs.ID+"/refresh", nil, nil)
	json.Unmarshal(body, &refreshed)
	if refreshed.Event.Kind != "unchanged" {
		t.Fatalf("second refresh: %s", body)
	}

	// manual edits: drop one network, add another
	resp, body = e.do("POST", p+"/rulesets/"+rs.ID+"/entries", map[string]any{"add": []string{"77.88.8.8"}, "remove": []string{"9.9.9.0/24"}}, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("edit: %d %s", resp.StatusCode, body)
	}
	_, txt = e.do("GET", "/r/moi-ip.txt", nil, nil)
	if string(txt) != "1.0.0.0/23\n8.8.8.8/32\n77.88.8.8/32\n" {
		t.Fatalf("after edit = %q", txt)
	}
	_, body = e.do("GET", p+"/rulesets/"+rs.ID+"/entries?q=77.", nil, nil)
	if !strings.Contains(string(body), `"total":1`) {
		t.Fatalf("entries search = %s", body)
	}

	// private set: link needs the token
	in["private"] = true
	resp, body = e.do("PUT", p+"/rulesets/"+rs.ID, in, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("update: %d %s", resp.StatusCode, body)
	}
	var priv struct{ Token string }
	json.Unmarshal(body, &priv)
	time.Sleep(300 * time.Millisecond)
	if r, _ := e.do("GET", "/r/moi-ip.txt", nil, nil); r.StatusCode != 404 {
		t.Fatalf("private without token = %d", r.StatusCode)
	}
	if r, _ := e.do("GET", "/r/moi-ip.txt?token="+priv.Token, nil, nil); r.StatusCode != 200 {
		t.Fatalf("private with token = %d", r.StatusCode)
	}

	if r, _ := e.do("DELETE", p+"/rulesets/"+rs.ID, nil, nil); r.StatusCode != 204 {
		t.Fatalf("delete = %d", r.StatusCode)
	}
	if r, _ := e.do("GET", "/r/moi-ip.txt?token="+priv.Token, nil, nil); r.StatusCode != 404 {
		t.Fatalf("after delete = %d", r.StatusCode)
	}
}

func TestStorageAndConvert(t *testing.T) {
	e := newEnv(t, "/")
	e.login("/")
	if r, b := e.do("POST", "/api/buckets", map[string]any{"name": "assets", "public": true}, nil); r.StatusCode != 201 {
		t.Fatalf("bucket: %d %s", r.StatusCode, b)
	}
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)
	body, ct := multipartBody(map[string]string{"prefix": "img"}, "file", "logo.svg", svg)
	r, b := e.do("POST", "/api/buckets/assets/upload", body, map[string]string{"Content-Type": ct})
	if r.StatusCode != 201 {
		t.Fatalf("upload: %d %s", r.StatusCode, b)
	}
	r, got := e.do("GET", "/f/assets/img/logo.svg", nil, nil)
	if r.StatusCode != 200 || !bytes.Equal(got, svg) || r.Header.Get("Content-Type") != "image/svg+xml" {
		t.Fatalf("public file: %d %s", r.StatusCode, r.Header.Get("Content-Type"))
	}
	if csp := r.Header.Get("Content-Security-Policy"); !strings.HasPrefix(csp, "sandbox") {
		t.Fatalf("svg served without sandbox: %q", csp)
	}
	// traversal attempts never escape
	for _, bad := range []string{"/f/assets/../mrsmith.db", "/f/assets/img/..%2f..%2fx", "/f/assets/%2e%2e/x"} {
		if r, _ := e.do("GET", bad, nil, nil); r.StatusCode != 404 {
			t.Fatalf("%s = %d", bad, r.StatusCode)
		}
	}
	_, b = e.do("GET", "/api/buckets/assets/list?prefix=", nil, nil)
	if !strings.Contains(string(b), `"folders":["img/"]`) {
		t.Fatalf("list = %s", b)
	}
	// private bucket is not public
	e.do("PATCH", "/api/buckets/assets", map[string]any{"public": false}, nil)
	if r, _ := e.do("GET", "/f/assets/img/logo.svg", nil, nil); r.StatusCode != 404 {
		t.Fatalf("private bucket = %d", r.StatusCode)
	}

	// convert pasted text to MRS
	text := "DOMAIN-SUFFIX,google.com\nDOMAIN,mail.google.com\n+.youtube.com\n"
	body, ct = multipartBody(map[string]string{"mode": "inspect", "source": "text", "optimize": "true", "text": text}, "", "", nil)
	r, b = e.do("POST", "/api/convert", body, map[string]string{"Content-Type": ct})
	var sum convertSummary
	json.Unmarshal(b, &sum)
	if r.StatusCode != 200 || sum.Result.Behavior != "domain" || sum.Result.Count != 2 || !sum.Result.Supported["mrs"] {
		t.Fatalf("inspect: %d %s", r.StatusCode, b)
	}
	body, ct = multipartBody(map[string]string{"mode": "download", "source": "text", "target": "mrs", "text": text}, "", "", nil)
	r, b = e.do("POST", "/api/convert", body, map[string]string{"Content-Type": ct})
	if m, err := rules.DecodeMRS(b); r.StatusCode != 200 || err != nil || len(m.Domains) != 3 {
		t.Fatalf("download mrs: %d %v", r.StatusCode, err)
	}
	// and back from MRS to yaml
	body, ct = multipartBody(map[string]string{"mode": "download", "source": "file", "target": "yaml"}, "file", "set.mrs", b)
	r, y := e.do("POST", "/api/convert", body, map[string]string{"Content-Type": ct})
	if r.StatusCode != 200 || !strings.Contains(string(y), "- '+.google.com'") {
		t.Fatalf("mrs->yaml: %d %s", r.StatusCode, y)
	}
	if !strings.Contains(r.Header.Get("Content-Disposition"), "set.yaml") {
		t.Fatalf("disposition = %s", r.Header.Get("Content-Disposition"))
	}
}

func TestSPAAndHeaders(t *testing.T) {
	e := newEnv(t, "/hidden/")
	r, b := e.do("GET", "/hidden/rulesets/abc", nil, nil)
	if r.StatusCode != 200 || !strings.Contains(string(b), `<base href="/hidden/">`) {
		t.Fatalf("spa fallback: %d %s", r.StatusCode, b)
	}
	if csp := r.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "script-src 'self'") || r.Header.Get("X-Frame-Options") != "DENY" {
		t.Fatalf("headers: %v", r.Header)
	}
	if r, _ := e.do("GET", "/", nil, nil); r.StatusCode != 404 {
		t.Fatalf("root with hidden panel = %d", r.StatusCode)
	}
	if r, _ := e.do("GET", "/hidden/assets/missing.js", nil, nil); r.StatusCode != 404 {
		t.Fatalf("missing asset = %d", r.StatusCode)
	}
	if r, _ := e.do("GET", "/healthz", nil, nil); r.StatusCode != 200 {
		t.Fatalf("healthz = %d", r.StatusCode)
	}
}

func TestLoginRequiresHTTPSBehindProxy(t *testing.T) {
	e := newEnv(t, "/")
	e.srvCfg().CookieSecure = true
	body := e.creds()
	// plain HTTP on a public host name: refused with a hint
	r, b := e.do("POST", "/api/auth/login", body, map[string]string{"Host": "rules.example.com"})
	if r.StatusCode != 400 || !strings.Contains(string(b), "HTTPS") {
		t.Fatalf("http login = %d %s", r.StatusCode, b)
	}
	// the same request forwarded by a trusted TLS proxy is accepted
	r, b = e.do("POST", "/api/auth/login", body, map[string]string{"Host": "rules.example.com", "X-Forwarded-Proto": "https"})
	if r.StatusCode != 200 || !strings.Contains(r.Header.Get("Set-Cookie"), "__Host-mrsmith=") {
		t.Fatalf("proxied login = %d %s", r.StatusCode, b)
	}
}

func TestGeneratedAdminFlow(t *testing.T) {
	e := newEnv(t, "/")
	e.login("/")
	var me struct {
		User struct {
			Username           string
			MustChangePassword bool
		}
	}
	_, b := e.do("GET", "/api/auth/me", nil, nil)
	json.Unmarshal(b, &me)
	if !me.User.MustChangePassword || me.User.Username != e.user {
		t.Fatalf("me = %s", b)
	}
	// rename needs the current password
	if r, _ := e.do("POST", "/api/auth/username", map[string]string{"username": "owner", "password": "wrong-one-1"}, nil); r.StatusCode != 400 {
		t.Fatalf("rename with wrong password = %d", r.StatusCode)
	}
	if r, b := e.do("POST", "/api/auth/username", map[string]string{"username": "owner", "password": e.pass}, nil); r.StatusCode != 204 {
		t.Fatalf("rename = %d %s", r.StatusCode, b)
	}
	if r, b := e.do("POST", "/api/auth/password", map[string]string{"current": e.pass, "next": "a much better passphrase"}, nil); r.StatusCode != 204 {
		t.Fatalf("password = %d %s", r.StatusCode, b)
	}
	_, b = e.do("GET", "/api/auth/me", nil, nil)
	json.Unmarshal(b, &me)
	if me.User.MustChangePassword || me.User.Username != "owner" {
		t.Fatalf("after changes me = %s", b)
	}
	// the old login no longer works, the new one does
	e.do("POST", "/api/auth/logout", nil, nil)
	if r, _ := e.do("POST", "/api/auth/login", map[string]string{"username": e.user, "password": "a much better passphrase"}, nil); r.StatusCode != 401 {
		t.Fatalf("old login = %d", r.StatusCode)
	}
	if r, _ := e.do("POST", "/api/auth/login", map[string]string{"username": "Owner", "password": "a much better passphrase"}, nil); r.StatusCode != 200 {
		t.Fatalf("new login = %d", r.StatusCode)
	}
}

func TestAudit(t *testing.T) {
	e := newEnv(t, "/")
	e.login("/")
	waitBuilt := func(id string) {
		for i := 0; i < 100; i++ {
			_, b := e.do("GET", "/api/rulesets/"+id, nil, nil)
			if !strings.Contains(string(b), `"status":"pending"`) && !strings.Contains(string(b), `"running":true`) {
				return
			}
			time.Sleep(30 * time.Millisecond)
		}
		t.Fatal("build did not finish")
	}
	mk := func(name string, include []string) string {
		in := map[string]any{"name": name, "formats": []string{"txt"}, "include": include, "behavior": "classical"}
		r, b := e.do("POST", "/api/rulesets", in, nil)
		if r.StatusCode != 201 {
			t.Fatalf("create %s: %d %s", name, r.StatusCode, b)
		}
		var rs struct{ ID string }
		json.Unmarshal(b, &rs)
		waitBuilt(rs.ID)
		return rs.ID
	}
	aID := mk("Twitch", []string{"admin.twitch.tv", "+.example.com", "1.2.3.0/24", "DOMAIN-KEYWORD,ads"})
	mk("Wide", []string{"+.twitch.tv", "+.example.com", "1.2.0.0/16"})
	e.do("POST", "/api/buckets", map[string]any{"name": "rules"}, nil)
	body, ct := multipartBody(nil, "file", "mixed.list", []byte("DOMAIN-SUFFIX,example.com\nDOMAIN,mail.example.com\nDOMAIN-KEYWORD,ads\n"))
	if r, b := e.do("POST", "/api/buckets/rules/upload", body, map[string]string{"Content-Type": ct}); r.StatusCode != 201 {
		t.Fatalf("upload: %d %s", r.StatusCode, b)
	}

	type report struct {
		ID        string
		Providers []struct{ ID, Name string }
		Totals    struct{ Providers, Same, Covered, Inner, Removable int }
		Pairs     []struct{ From, To, Same, Covered, Inner int }
	}
	run := func() report {
		r, b := e.do("POST", "/api/audit", map[string]any{"providers": []string{}}, nil)
		if r.StatusCode != 200 {
			t.Fatalf("audit: %d %s", r.StatusCode, b)
		}
		var rep report
		json.Unmarshal(b, &rep)
		return rep
	}
	rep := run()
	if rep.Totals.Providers != 3 {
		t.Fatalf("providers = %+v", rep.Providers)
	}
	idx := map[string]int{}
	for i, p := range rep.Providers {
		idx[p.Name] = i
	}
	// same: +.example.com (3 places), DOMAIN-KEYWORD,ads (2); covered: admin.twitch.tv, 1.2.3.0/24; inner: mail.example.com
	if rep.Totals.Same != 2 || rep.Totals.Covered != 2 || rep.Totals.Inner != 1 {
		t.Fatalf("totals = %+v", rep.Totals)
	}
	_, b := e.do("GET", "/api/audit/"+rep.ID+"/findings?q=twitch", nil, nil)
	if !strings.Contains(string(b), `"value":"admin.twitch.tv"`) || !strings.Contains(string(b), `"rule":"+.twitch.tv"`) {
		t.Fatalf("findings for twitch: %s", b)
	}

	// remove from "Twitch" everything that "Wide" already has
	r, b := e.do("POST", "/api/audit/"+rep.ID+"/fix", map[string]any{"pair": map[string]int{"from": idx["Twitch"], "to": idx["Wide"]}}, nil)
	if r.StatusCode != 200 {
		t.Fatalf("fix pair: %d %s", r.StatusCode, b)
	}
	waitBuilt(aID)
	_, b = e.do("GET", "/api/rulesets/"+aID+"/entries?limit=50", nil, nil)
	for _, gone := range []string{"admin.twitch.tv", "1.2.3.0/24"} {
		if strings.Contains(string(b), gone) {
			t.Fatalf("%s still in Twitch: %s", gone, b)
		}
	}
	if !strings.Contains(string(b), "DOMAIN-KEYWORD,ads") {
		t.Fatalf("unrelated rule removed: %s", b)
	}
	// the old report is gone after a fix
	if r, _ := e.do("GET", "/api/audit/"+rep.ID, nil, nil); r.StatusCode != 404 {
		t.Fatalf("stale report = %d", r.StatusCode)
	}

	// clean redundancy inside the stored file: it keeps its classical notation
	rep = run()
	r, b = e.do("POST", "/api/audit/"+rep.ID+"/fix", map[string]any{"inner": []int{idx["mixed.list"]}}, nil)
	if r.StatusCode != 200 || !strings.Contains(string(b), `"action":"rewritten"`) {
		t.Fatalf("fix inner: %d %s", r.StatusCode, b)
	}
	_, file := e.do("GET", "/api/buckets/rules/object?key=mixed.list", nil, nil)
	if string(file) != "DOMAIN-SUFFIX,example.com\nDOMAIN-KEYWORD,ads\n" {
		t.Fatalf("rewritten file: %q", file)
	}
	rep = run()
	if rep.Totals.Covered != 0 || rep.Totals.Inner != 0 {
		t.Fatalf("after fixes totals = %+v", rep.Totals)
	}
}

func TestAuditBroadAndExclusionPreview(t *testing.T) {
	e := newEnv(t, "/")
	e.login("/")
	create := func(name string, include []string) string {
		r, b := e.do("POST", "/api/rulesets", map[string]any{"name": name, "formats": []string{"txt"}, "include": include}, nil)
		if r.StatusCode != 201 {
			t.Fatalf("create: %d %s", r.StatusCode, b)
		}
		var rs struct{ ID string }
		json.Unmarshal(b, &rs)
		for i := 0; i < 100; i++ {
			_, b := e.do("GET", "/api/rulesets/"+rs.ID, nil, nil)
			if strings.Contains(string(b), `"status":"ok"`) && !strings.Contains(string(b), `"running":true`) {
				break
			}
			time.Sleep(30 * time.Millisecond)
		}
		return rs.ID
	}
	narrow := create("Narrow", []string{"link.example.com", "other.org"})
	wide := create("Wide", []string{"+.example.com", "+.keep.net"})

	_, b := e.do("POST", "/api/audit", map[string]any{}, nil)
	var rep struct{ ID string }
	json.Unmarshal(b, &rep)
	_, b = e.do("GET", "/api/audit/"+rep.ID+"/findings?kind=covered", nil, nil)
	var page struct{ Items []struct{ ID int } }
	json.Unmarshal(b, &page)
	if len(page.Items) != 1 {
		t.Fatalf("covered findings: %s", b)
	}
	// keep the narrow rule, remove the broad one
	fid := strconv.Itoa(page.Items[0].ID)
	r, b := e.do("POST", "/api/audit/"+rep.ID+"/fix", map[string]any{"findings": []int{page.Items[0].ID}, "broad": map[string]bool{fid: true}}, nil)
	if r.StatusCode != 200 {
		t.Fatalf("fix broad: %d %s", r.StatusCode, b)
	}
	_, b = e.do("GET", "/api/rulesets/"+wide+"/entries", nil, nil)
	if strings.Contains(string(b), "+.example.com") || !strings.Contains(string(b), "+.keep.net") {
		t.Fatalf("wide after broad fix: %s", b)
	}
	_, b = e.do("GET", "/api/rulesets/"+narrow+"/entries", nil, nil)
	if !strings.Contains(string(b), "link.example.com") {
		t.Fatalf("narrow lost its rule: %s", b)
	}
	_, b = e.do("GET", "/api/rulesets/"+wide, nil, nil)
	if !strings.Contains(string(b), `"exclude":[{"rule":"+.example.com","mode":"exact"}]`) {
		t.Fatalf("exclusion not recorded as exact: %s", b)
	}

	// preview: excluding a host with "parents" would also drop its parents
	e.do("POST", "/api/rulesets/"+narrow+"/entries", map[string]any{"add": []string{"+.example.com"}}, nil)
	r, b = e.do("POST", "/api/rulesets/"+narrow+"/exclude/preview", map[string]string{"rule": "link.example.com", "mode": "parents"}, nil)
	if r.StatusCode != 200 || !strings.Contains(string(b), `"removed":["+.example.com","link.example.com"]`) {
		t.Fatalf("preview parents: %d %s", r.StatusCode, b)
	}
	_, b = e.do("POST", "/api/rulesets/"+narrow+"/exclude/preview", map[string]string{"rule": "link.example.com", "mode": "exact"}, nil)
	if !strings.Contains(string(b), `"still":{"rule":"link.example.com","by":"+.example.com"}`) {
		t.Fatalf("preview exact should warn about the parent: %s", b)
	}
}

func TestLookup(t *testing.T) {
	e := newEnv(t, "/")
	e.login("/")
	r, b := e.do("POST", "/api/rulesets", map[string]any{"name": "Streams", "formats": []string{"txt"}, "behavior": "classical",
		"include": []string{"+.twitch.tv", "admin.twitch.tv", "DOMAIN-KEYWORD,twitch", "10.0.0.0/8"}}, nil)
	if r.StatusCode != 201 {
		t.Fatalf("create: %s", b)
	}
	e.do("POST", "/api/buckets", map[string]any{"name": "rules"}, nil)
	body, ct := multipartBody(nil, "file", "list.txt", []byte("+.tv\nexample.org\n10.1.2.0/24\n"))
	e.do("POST", "/api/buckets/rules/upload", body, map[string]string{"Content-Type": ct})
	time.Sleep(300 * time.Millisecond)

	get := func(q string) string {
		r, b := e.do("GET", "/api/audit/lookup?q="+url.QueryEscape(q), nil, nil)
		if r.StatusCode != 200 {
			t.Fatalf("lookup %s: %d %s", q, r.StatusCode, b)
		}
		return string(b)
	}
	got := get("admin.twitch.tv")
	for _, want := range []string{`"mode":"domain"`, `{"rule":"admin.twitch.tv","relation":"match"}`, `{"rule":"+.twitch.tv","relation":"match"}`,
		`{"rule":"DOMAIN-KEYWORD,twitch","relation":"match"}`, `{"rule":"+.tv","relation":"match"}`} {
		if !strings.Contains(got, want) {
			t.Fatalf("lookup host missing %s in %s", want, got)
		}
	}
	if got := get("twitch.tv"); !strings.Contains(got, `{"rule":"admin.twitch.tv","relation":"inside"}`) {
		t.Fatalf("lookup parent: %s", got)
	}
	got = get("10.1.2.3")
	if !strings.Contains(got, `{"rule":"10.0.0.0/8","relation":"match"}`) || !strings.Contains(got, `{"rule":"10.1.2.0/24","relation":"match"}`) {
		t.Fatalf("lookup ip: %s", got)
	}
	if got := get("example"); !strings.Contains(got, `"mode":"text"`) || !strings.Contains(got, "example.org") {
		t.Fatalf("lookup text: %s", got)
	}
}

func TestDownloadsBucketLifecycle(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "+.example"+strings.ReplaceAll(r.URL.Path, "/", "-")+".com\n")
	}))
	defer upstream.Close()
	e := newEnv(t, "/")
	e.login("/")
	list := func(prefix string) string {
		_, b := e.do("GET", "/api/buckets/downloads/list?prefix="+url.QueryEscape(prefix), nil, nil)
		return string(b)
	}
	wait := func(id string) {
		for i := 0; i < 100; i++ {
			_, b := e.do("GET", "/api/rulesets/"+id, nil, nil)
			if strings.Contains(string(b), `"status":"ok"`) && !strings.Contains(string(b), `"running":true`) {
				return
			}
			time.Sleep(30 * time.Millisecond)
		}
		t.Fatal("build did not finish")
	}
	in := map[string]any{"name": "Ads", "formats": []string{"txt"}, "sources": []map[string]any{
		{"type": "url", "url": upstream.URL + "/a/list.txt"},
		{"type": "url", "url": upstream.URL + "/b/list.txt"},
	}}
	_, b := e.do("POST", "/api/rulesets", in, nil)
	var rs struct {
		ID      string
		Sources []struct{ ID string }
	}
	json.Unmarshal(b, &rs)
	wait(rs.ID)
	got := list("ads/")
	if !strings.Contains(got, `"key":"ads/list.txt"`) || !strings.Contains(got, `"key":"ads/list-2.txt"`) || !strings.Contains(got, `"origin":"`+upstream.URL+`/a/list.txt"`) {
		t.Fatalf("downloads after create: %s", got)
	}
	// the bucket is read-only for people
	body, ct := multipartBody(nil, "file", "x.txt", []byte("x"))
	if r, _ := e.do("POST", "/api/buckets/downloads/upload", body, map[string]string{"Content-Type": ct}); r.StatusCode != 403 {
		t.Fatalf("upload into downloads = %d", r.StatusCode)
	}
	if r, _ := e.do("POST", "/api/buckets/downloads/delete", map[string]any{"keys": []string{"ads/list.txt"}}, nil); r.StatusCode != 403 {
		t.Fatalf("delete in downloads = %d", r.StatusCode)
	}
	if r, _ := e.do("DELETE", "/api/buckets/downloads?force=1", nil, nil); r.StatusCode == 204 {
		t.Fatal("downloads bucket was deleted")
	}

	// replace the second link and rename the set: the old copy goes away, the folder moves
	_, b = e.do("GET", "/api/rulesets/"+rs.ID, nil, nil)
	json.Unmarshal(b, &rs)
	in["slug"] = "ads-v2"
	in["sources"] = []map[string]any{
		{"id": rs.Sources[0].ID, "type": "url", "url": upstream.URL + "/a/list.txt"},
		{"type": "url", "url": upstream.URL + "/c/hosts"},
	}
	if r, b := e.do("PUT", "/api/rulesets/"+rs.ID, in, nil); r.StatusCode != 200 {
		t.Fatalf("update: %s", b)
	}
	wait(rs.ID)
	if got := list("ads/"); strings.Contains(got, `"key":"ads/`) {
		t.Fatalf("old folder kept: %s", got)
	}
	got = list("ads-v2/")
	if !strings.Contains(got, `"key":"ads-v2/list.txt"`) || !strings.Contains(got, `"key":"ads-v2/hosts"`) || strings.Contains(got, "list-2") {
		t.Fatalf("downloads after update: %s", got)
	}

	// an uploaded source file disappears once no set uses it
	body, ct = multipartBody(nil, "file", "mine.txt", []byte("+.mine.org\n"))
	_, b = e.do("POST", "/api/rulesets/sources/upload", body, map[string]string{"Content-Type": ct})
	var up struct{ Bucket, Key string }
	json.Unmarshal(b, &up)
	in["sources"] = []map[string]any{{"type": "object", "bucket": up.Bucket, "key": up.Key}}
	e.do("PUT", "/api/rulesets/"+rs.ID, in, nil)
	wait(rs.ID)
	if got := list("ads-v2/"); strings.Contains(got, `"key":"ads-v2/`) {
		t.Fatalf("copies of removed links kept: %s", got)
	}
	if r, _ := e.do("DELETE", "/api/rulesets/"+rs.ID, nil, nil); r.StatusCode != 204 {
		t.Fatal("delete failed")
	}
	_, b = e.do("GET", "/api/buckets/rule-sources/list", nil, nil)
	if strings.Contains(string(b), up.Key) {
		t.Fatalf("unused upload kept: %s", b)
	}
}
