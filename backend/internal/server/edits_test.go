package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type stateView struct {
	ID      string
	Slug    string
	Include []string
	Exclude []struct{ Rule, Mode string }
	Sources []struct{ ID string }
	State   struct {
		Status     string
		Count      int
		LastRunAt  time.Time
		NextRunAt  time.Time
		Hash       string
		DurationMs int64
	}
}

func (e *env) entries(id string) []string {
	e.t.Helper()
	_, body := e.do("GET", "/api/rulesets/"+id+"/entries?limit=500", nil, nil)
	var page struct{ Items []string }
	json.Unmarshal(body, &page)
	return page.Items
}

func TestEditsKeepTimerAndReplaceRules(t *testing.T) {
	var fetches atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetches.Add(1)
		w.Header().Set("ETag", `"v1"`)
		if r.Header.Get("If-None-Match") == `"v1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		io.WriteString(w, "a.example\n+.b.example\n")
	}))
	defer upstream.Close()

	e := newEnv(t, "/")
	e.login("/")
	_, body := e.do("POST", "/api/rulesets", map[string]any{
		"name": "Таймер", "slug": "timer", "behavior": "domain", "formats": []string{"txt"}, "intervalMin": 60,
		"sources": []map[string]any{{"type": "url", "url": upstream.URL + "/list.txt"}},
	}, nil)
	var rs stateView
	json.Unmarshal(body, &rs)
	json.Unmarshal(e.waitBuilt("/api", rs.ID), &rs)
	firstRun, nextRun := rs.State.LastRunAt, rs.State.NextRunAt
	if firstRun.IsZero() || nextRun.Sub(firstRun) != time.Hour {
		t.Fatalf("timer after first build: %v → %v", firstRun, nextRun)
	}

	// Edits rebuild from the downloaded copy and leave the timer alone.
	time.Sleep(20 * time.Millisecond)
	e.do("POST", "/api/rulesets/"+rs.ID+"/entries", map[string]any{"add": []string{"manual.example"}}, nil)
	r, body := e.do("POST", "/api/rulesets/"+rs.ID+"/entries/replace", map[string]string{"from": "a.example", "to": "c.example"}, nil)
	if r.StatusCode != 200 {
		t.Fatalf("replace source rule: %d %s", r.StatusCode, body)
	}
	r, body = e.do("POST", "/api/rulesets/"+rs.ID+"/entries/replace", map[string]string{"from": "manual.example", "to": "+.manual2.example"}, nil)
	if r.StatusCode != 200 {
		t.Fatalf("replace manual rule: %d %s", r.StatusCode, body)
	}
	json.Unmarshal(body, &rs)
	if !rs.State.LastRunAt.Equal(firstRun) || !rs.State.NextRunAt.Equal(nextRun) {
		t.Fatalf("an edit moved the timer: %v → %v", rs.State.LastRunAt, rs.State.NextRunAt)
	}
	got := e.entries(rs.ID)
	for _, want := range []string{"c.example", "+.b.example", "+.manual2.example"} {
		if !slices.Contains(got, want) {
			t.Fatalf("entries %v lack %s", got, want)
		}
	}
	if slices.Contains(got, "a.example") || slices.Contains(got, "manual.example") {
		t.Fatalf("old rules still present: %v", got)
	}
	if len(rs.Exclude) != 1 || rs.Exclude[0].Rule != "a.example" || rs.Exclude[0].Mode != "exact" {
		t.Fatalf("a manual rule left an exclusion behind: %+v", rs.Exclude)
	}
	if r, _ := e.do("POST", "/api/rulesets/"+rs.ID+"/entries/replace", map[string]string{"from": "c.example", "to": "c.example"}, nil); r.StatusCode != 400 {
		t.Fatalf("no-op replace = %d", r.StatusCode)
	}

	// Restore everything in one request.
	r, body = e.do("POST", "/api/rulesets/"+rs.ID+"/overrides/clear", map[string]any{"allInclude": true, "allExclude": true}, nil)
	if r.StatusCode != 200 {
		t.Fatalf("clear: %d %s", r.StatusCode, body)
	}
	json.Unmarshal(body, &rs)
	if len(rs.Include)+len(rs.Exclude) != 0 {
		t.Fatalf("overrides left: %+v %+v", rs.Include, rs.Exclude)
	}
	if got := e.entries(rs.ID); strings.Join(got, ",") != "a.example,+.b.example" {
		t.Fatalf("entries after restore: %v", got)
	}

	// A refresh checks the source and moves the timer; an unchanged source
	// does not rebuild the set.
	before := fetches.Load()
	r, body = e.do("POST", "/api/rulesets/"+rs.ID+"/refresh", nil, nil)
	var res struct {
		Ruleset stateView
		Event   struct{ Kind string }
	}
	json.Unmarshal(body, &res)
	if r.StatusCode != 200 || res.Event.Kind != "unchanged" || fetches.Load() != before+1 {
		t.Fatalf("refresh: %d %s", r.StatusCode, body)
	}
	if !res.Ruleset.State.LastRunAt.After(firstRun) {
		t.Fatal("a refresh did not move the timer")
	}
	if res.Ruleset.State.Hash != rs.State.Hash || res.Ruleset.State.Count != 2 {
		t.Fatalf("unchanged refresh altered the set: %+v", res.Ruleset.State)
	}

	// Public links keep working after a rename.
	in := map[string]any{
		"name": "Таймер", "slug": "timer-2", "behavior": "domain", "formats": []string{"txt"}, "intervalMin": 60,
		"sources": []map[string]any{{"id": rs.Sources[0].ID, "type": "url", "url": upstream.URL + "/list.txt"}},
	}
	if r, b := e.do("PUT", "/api/rulesets/"+rs.ID, in, nil); r.StatusCode != 200 {
		t.Fatalf("rename: %d %s", r.StatusCode, b)
	}
	e.waitBuilt("/api", rs.ID)
	if r, _ := e.do("GET", "/r/timer-2.txt", nil, nil); r.StatusCode != 200 {
		t.Fatalf("new link = %d", r.StatusCode)
	}
	if r, _ := e.do("GET", "/r/timer.txt", nil, nil); r.StatusCode != 404 {
		t.Fatalf("old link = %d", r.StatusCode)
	}
}

func TestListOmitsOverrides(t *testing.T) {
	e := newEnv(t, "/")
	e.login("/")
	_, body := e.do("POST", "/api/rulesets", map[string]any{
		"name": "Список", "behavior": "domain", "formats": []string{"txt"}, "include": []string{"a.example", "b.example"},
	}, nil)
	var rs stateView
	json.Unmarshal(body, &rs)
	e.waitBuilt("/api", rs.ID)
	_, body = e.do("GET", "/api/rulesets", nil, nil)
	if strings.Contains(string(body), "a.example") {
		t.Fatalf("list carries manual rules: %s", body)
	}
	_, body = e.do("GET", "/api/rulesets/"+rs.ID, nil, nil)
	if !strings.Contains(string(body), "a.example") {
		t.Fatalf("set page lacks manual rules: %s", body)
	}
}
