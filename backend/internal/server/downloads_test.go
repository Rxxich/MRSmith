package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"mrsmith/internal/rulesets"
)

type counter struct {
	Downloads int64
	Checks    int64
	LastAt    time.Time
}

func (e *env) waitBuilt(p, id string) []byte {
	e.t.Helper()
	var body []byte
	for i := 0; i < 100; i++ {
		_, body = e.do("GET", p+"/rulesets/"+id, nil, nil)
		var rs struct {
			State struct{ Status string }
		}
		json.Unmarshal(body, &rs)
		if rs.State.Status != "pending" {
			return body
		}
		time.Sleep(50 * time.Millisecond)
	}
	e.t.Fatalf("build did not finish: %s", body)
	return nil
}

func TestDownloadCounters(t *testing.T) {
	e := newEnv(t, "/")
	e.login("/")
	resp, body := e.do("POST", "/api/rulesets", map[string]any{
		"name": "Счётчик", "slug": "counted", "behavior": "domain", "formats": []string{"txt", "mrs"},
		"include": []string{"example.com", "+.example.org"},
	}, nil)
	if resp.StatusCode != 201 {
		t.Fatalf("create: %d %s", resp.StatusCode, body)
	}
	var rs struct{ ID string }
	json.Unmarshal(body, &rs)
	e.waitBuilt("/api", rs.ID)

	ua := map[string]string{"User-Agent": "mihomo/1.19.34"}
	r, _ := e.do("GET", "/r/counted.txt", nil, ua)
	etag := r.Header.Get("ETag")
	e.do("GET", "/r/counted.txt", nil, ua)
	if r, _ := e.do("GET", "/r/counted.txt", nil, map[string]string{"User-Agent": "mihomo/1.19.34", "If-None-Match": etag}); r.StatusCode != 304 {
		t.Fatalf("conditional request = %d", r.StatusCode)
	}
	e.do("HEAD", "/r/counted.txt", nil, ua)
	e.do("GET", "/r/counted.mrs", nil, map[string]string{"User-Agent": "clash-verge/v2.2.3"})
	e.do("GET", "/r/missing.txt", nil, ua)

	_, body = e.do("GET", "/api/rulesets/"+rs.ID, nil, nil)
	var got struct {
		Downloads map[string]counter
	}
	json.Unmarshal(body, &got)
	if c := got.Downloads["txt"]; c.Downloads != 2 || c.Checks != 1 || c.LastAt.IsZero() {
		t.Fatalf("txt counter %+v", c)
	}
	if c := got.Downloads["mrs"]; c.Downloads != 1 {
		t.Fatalf("mrs counter %+v", c)
	}

	// public bucket files
	e.do("POST", "/api/buckets", map[string]any{"name": "assets", "public": true}, nil)
	upload := func(content string) {
		b, ct := multipartBody(nil, "file", "list.txt", []byte(content))
		if r, out := e.do("POST", "/api/buckets/assets/upload", b, map[string]string{"Content-Type": ct}); r.StatusCode != 201 {
			t.Fatalf("upload: %d %s", r.StatusCode, out)
		}
	}
	upload("a.example\n")
	e.do("GET", "/f/assets/list.txt", nil, ua)
	e.do("GET", "/f/assets/list.txt", nil, ua)
	upload("a.example\nb.example\n") // a new version keeps the count
	e.do("POST", "/api/buckets/assets/rename", map[string]string{"from": "list.txt", "to": "renamed.txt"}, nil)
	_, body = e.do("GET", "/api/buckets/assets/list", nil, nil)
	var listing struct {
		Objects []struct {
			Key       string
			Downloads counter
		}
	}
	json.Unmarshal(body, &listing)
	if len(listing.Objects) != 1 || listing.Objects[0].Key != "renamed.txt" || listing.Objects[0].Downloads.Downloads != 2 {
		t.Fatalf("object counter after re-upload and rename: %s", body)
	}

	_, body = e.do("GET", "/api/downloads", nil, nil)
	var journal struct {
		Summary struct {
			Hits    int
			Clients []struct {
				IP, UserAgent     string
				Downloads, Checks int
				Files             []string
			}
		}
		Recent []struct{ Name string }
	}
	json.Unmarshal(body, &journal)
	if journal.Summary.Hits != 6 || len(journal.Summary.Clients) != 2 || len(journal.Recent) != 6 {
		t.Fatalf("journal: %s", body)
	}
	c := journal.Summary.Clients[0]
	if c.UserAgent != "mihomo/1.19.34" || c.IP != "127.0.0.1" || c.Downloads != 4 || c.Checks != 1 || len(c.Files) != 2 {
		t.Fatalf("latest client: %+v", c)
	}

	if r, _ := e.do("DELETE", "/api/downloads", nil, nil); r.StatusCode != 204 {
		t.Fatalf("clear = %d", r.StatusCode)
	}
	_, body = e.do("GET", "/api/downloads", nil, nil)
	json.Unmarshal(body, &journal)
	if journal.Summary.Hits != 0 {
		t.Fatalf("journal not cleared: %s", body)
	}
	_, body = e.do("GET", "/api/rulesets/"+rs.ID, nil, nil)
	json.Unmarshal(body, &got)
	if got.Downloads["txt"].Downloads != 2 {
		t.Fatal("clearing the journal reset the counters")
	}
}

func TestChangeNotice(t *testing.T) {
	var version atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "a.example\n")
		for i := int32(0); i < version.Load(); i++ {
			io.WriteString(w, string(rune('b'+i))+".example\n")
		}
	}))
	defer upstream.Close()

	e := newEnv(t, "/")
	var mu sync.Mutex
	var notices []rulesets.Event
	e.mgr.OnChange(func(_ *rulesets.Ruleset, ev *rulesets.Event) {
		mu.Lock()
		notices = append(notices, *ev)
		mu.Unlock()
	})
	e.login("/")
	in := map[string]any{
		"name": "Уведомления", "behavior": "domain", "formats": []string{"txt"}, "notify": true,
		"sources": []map[string]any{{"type": "url", "url": upstream.URL + "/list.txt"}},
	}
	_, body := e.do("POST", "/api/rulesets", in, nil)
	var rs struct {
		ID      string
		Sources []struct{ ID string }
	}
	json.Unmarshal(body, &rs)
	json.Unmarshal(e.waitBuilt("/api", rs.ID), &rs)

	refresh := func() {
		if r, b := e.do("POST", "/api/rulesets/"+rs.ID+"/refresh", nil, nil); r.StatusCode != 200 {
			t.Fatalf("refresh: %d %s", r.StatusCode, b)
		}
	}
	refresh() // nothing changed
	version.Store(1)
	refresh()
	mu.Lock()
	if len(notices) != 1 || notices[0].Added != 1 || notices[0].Trigger != "manual" {
		t.Fatalf("notices after one change: %+v", notices)
	}
	mu.Unlock()

	in["notify"] = false
	in["sources"] = []map[string]any{{"id": rs.Sources[0].ID, "type": "url", "url": upstream.URL + "/list.txt"}}
	if r, b := e.do("PUT", "/api/rulesets/"+rs.ID, in, nil); r.StatusCode != 200 {
		t.Fatalf("update: %d %s", r.StatusCode, b)
	}
	e.waitBuilt("/api", rs.ID)
	version.Store(2)
	refresh()
	mu.Lock()
	defer mu.Unlock()
	if len(notices) != 1 {
		t.Fatalf("a set with notices off still notified: %+v", notices)
	}
}
