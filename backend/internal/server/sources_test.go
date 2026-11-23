package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// A link that never answers must not hold up the panel: other sets keep
// building, and editing or deleting the stuck set stops its download.
func TestStuckSourceDoesNotBlock(t *testing.T) {
	arrived := make(chan struct{}, 10)
	release := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("/stuck", func(w http.ResponseWriter, r *http.Request) {
		arrived <- struct{}{}
		select {
		case <-r.Context().Done():
		case <-release:
		}
	})
	mux.HandleFunc("/good", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "+.good.example\n") })
	src := httptest.NewServer(mux)
	defer src.Close()
	defer close(release)

	e := newEnv(t, "/")
	e.login("/")
	create := func(name, path string) string {
		resp, body := e.do("POST", "/api/rulesets", map[string]any{
			"name": name, "formats": []string{"yaml"}, "sources": []map[string]any{{"type": "url", "url": src.URL + path}},
		}, nil)
		if resp.StatusCode != 201 {
			t.Fatalf("create %s: %d %s", name, resp.StatusCode, body)
		}
		var rs struct{ ID string }
		json.Unmarshal(body, &rs)
		return rs.ID
	}
	other := create("other", "/good")
	e.waitBuilt("/api", other)

	stuck1, stuck2 := create("stuck-1", "/stuck"), create("stuck-2", "/stuck")
	for i := 0; i < 2; i++ {
		select {
		case <-arrived:
		case <-time.After(5 * time.Second):
			t.Fatal("stuck downloads did not start")
		}
	}
	quick := func(what string, fn func() int, want int) {
		t.Helper()
		start := time.Now()
		if code := fn(); code != want {
			t.Fatalf("%s: %d, want %d", what, code, want)
		}
		if d := time.Since(start); d > 2*time.Second {
			t.Fatalf("%s took %v while downloads hang", what, d)
		}
	}
	// Builds are not stuck behind the downloads.
	quick("edit another set", func() int {
		r, _ := e.do("POST", "/api/rulesets/"+other+"/entries", map[string]any{"add": []string{"+.more.example"}}, nil)
		return r.StatusCode
	}, 200)
	// Fixing the link stops the stuck download.
	quick("fix the stuck link", func() int {
		r, _ := e.do("PUT", "/api/rulesets/"+stuck1, map[string]any{
			"name": "stuck-1", "formats": []string{"yaml"}, "sources": []map[string]any{{"type": "url", "url": src.URL + "/good"}},
		}, nil)
		return r.StatusCode
	}, 200)
	quick("delete a stuck set", func() int { r, _ := e.do("DELETE", "/api/rulesets/"+stuck2, nil, nil); return r.StatusCode }, 204)

	var rs struct {
		State   struct{ Status string }
		Sources []struct{ URL string }
	}
	json.Unmarshal(e.waitBuilt("/api", stuck1), &rs)
	if rs.State.Status != "ok" || !strings.HasSuffix(rs.Sources[0].URL, "/good") {
		t.Fatalf("fixed set: %+v", rs)
	}
	_, body := e.do("GET", "/api/rulesets/"+stuck1+"/events", nil, nil)
	if strings.Contains(string(body), "прервано") || strings.Contains(string(body), "не загрузился") {
		t.Fatalf("the stopped download left an error in the history: %s", body)
	}
}

func TestUnsuitableSources(t *testing.T) {
	pages := map[string]string{
		"/page.html":  "<!DOCTYPE html>\n<html><head><title>twitch.list at meta</title></head><body>\ngithub.com\n</body></html>\n",
		"/rules.json": `{"version":2,"rules":[{"domain_suffix":["twitch.tv"]}]}`,
		"/empty.txt":  "",
	}
	src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := pages[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		io.WriteString(w, body)
	}))
	defer src.Close()
	e := newEnv(t, "/")
	e.login("/")
	for path, want := range map[string]string{
		"/page.html":  "веб-страница (HTML)",
		"/rules.json": "JSON",
		"/missing":    "404",
		"/empty.txt":  "",
	} {
		_, body := e.do("POST", "/api/rulesets", map[string]any{
			"name": path, "formats": []string{"mrs"}, "sources": []map[string]any{{"type": "url", "url": src.URL + path}},
		}, nil)
		var rs struct{ ID string }
		json.Unmarshal(body, &rs)
		var got struct {
			State   struct{ Status, Error string }
			Sources []struct {
				State struct{ Error string }
			}
		}
		json.Unmarshal(e.waitBuilt("/api", rs.ID), &got)
		if got.State.Status != "error" || !strings.Contains(got.Sources[0].State.Error, want) || !strings.Contains(got.State.Error, "ещё не собраны") {
			t.Fatalf("%s: %+v", path, got)
		}
		if r, _ := e.do("GET", "/r/"+strings.Trim(strings.ReplaceAll(path, ".", "-"), "/")+".mrs", nil, nil); r.StatusCode != 404 {
			t.Fatalf("%s: a broken set published a file", path)
		}
	}
}
