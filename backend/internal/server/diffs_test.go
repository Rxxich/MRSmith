package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestFullChangeList(t *testing.T) {
	var round atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Round 0: d0..d49. Round 1: d10..d149 — 100 added, 10 removed.
		from, to := 0, 50
		if round.Load() > 0 {
			from, to = 10, 150
		}
		for i := from; i < to; i++ {
			fmt.Fprintf(w, "d%d.example\n", i)
		}
	}))
	defer upstream.Close()
	e := newEnv(t, "/")
	e.login("/")
	_, body := e.do("POST", "/api/rulesets", map[string]any{
		"name": "Список", "behavior": "domain", "formats": []string{"txt"},
		"sources": []map[string]any{{"type": "url", "url": upstream.URL + "/list.txt"}},
	}, nil)
	var rs struct{ ID string }
	json.Unmarshal(body, &rs)
	e.waitBuilt("/api", rs.ID)
	round.Store(1)
	if r, b := e.do("POST", "/api/rulesets/"+rs.ID+"/refresh", nil, nil); r.StatusCode != 200 {
		t.Fatalf("refresh: %d %s", r.StatusCode, b)
	}

	_, body = e.do("GET", "/api/rulesets/"+rs.ID+"/events", nil, nil)
	var evs []struct {
		ID, Kind, Trigger string
		Added, Removed    int
		HasDiff           bool
	}
	json.Unmarshal(body, &evs)
	var evID string
	for _, ev := range evs {
		switch ev.Trigger {
		case "manual":
			if !ev.HasDiff || ev.Added != 100 || ev.Removed != 10 {
				t.Fatalf("changed event: %+v", ev)
			}
			evID = ev.ID
		case "create":
			if ev.HasDiff {
				t.Fatal("the first build kept a full list")
			}
		}
	}
	type page struct {
		Kind                  string
		Added, Removed, Total int
		Items                 []string
	}
	get := func(query string) page {
		r, b := e.do("GET", "/api/rulesets/"+rs.ID+"/events/"+evID+"/diff"+query, nil, nil)
		if r.StatusCode != 200 {
			t.Fatalf("diff %s: %d %s", query, r.StatusCode, b)
		}
		var p page
		json.Unmarshal(b, &p)
		return p
	}
	if p := get("?kind=added&limit=30&offset=90"); p.Added != 100 || p.Removed != 10 || p.Total != 100 || len(p.Items) != 10 {
		t.Fatalf("added, last page: %+v", p)
	}
	if p := get("?kind=removed"); p.Total != 10 || p.Items[0] != "d0.example" {
		t.Fatalf("removed: %+v", p)
	}
	if p := get("?kind=added&q=D14"); p.Total != 10 { // d14, d140..d149 minus d14 (was there) = d140..d149
		t.Fatalf("search: %+v", p)
	}
	for _, bad := range []string{"/events/" + evID + "/diff?kind=both", "/events/zzz/diff", "/events/" + evID[:23] + "0/diff"} {
		if r, _ := e.do("GET", "/api/rulesets/"+rs.ID+bad, nil, nil); r.StatusCode != 400 && r.StatusCode != 404 {
			t.Fatalf("%s: %d", bad, r.StatusCode)
		}
	}
	// Deleting the set removes its lists.
	e.do("DELETE", "/api/rulesets/"+rs.ID, nil, nil)
	if r, _ := e.do("GET", "/api/rulesets/"+rs.ID+"/events/"+evID+"/diff", nil, nil); r.StatusCode != 404 {
		t.Fatalf("list of a deleted set: %d", r.StatusCode)
	}
}
