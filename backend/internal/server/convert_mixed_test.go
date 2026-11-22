package server

import (
	"encoding/json"
	"strings"
	"testing"

	"mrsmith/internal/rules"
)

const mixedYAML = `payload:
  - DOMAIN-SUFFIX,ua
  - DOMAIN-SUFFIX,lib.social
  - DOMAIN-KEYWORD,torrent
  - DOMAIN-REGEX,^(.*\.)?riotgames\.com$
  - IP-CIDR,1.2.3.0/24,no-resolve
`

func TestConvertMixedListToMRS(t *testing.T) {
	e := newEnv(t, "/")
	e.login("/")
	body, ct := multipartBody(map[string]string{"source": "text", "text": mixedYAML, "mode": "inspect", "target": "mrs"}, "", "", nil)
	r, out := e.do("POST", "/api/convert", body, map[string]string{"Content-Type": ct})
	if r.StatusCode != 200 {
		t.Fatalf("inspect: %d %s", r.StatusCode, out)
	}
	var sum struct {
		Result struct {
			Behavior  string
			Supported map[string]bool
			MRS       *struct {
				Behavior   string
				Count      int
				Translated int
				Dropped    map[string]int
			}
		}
	}
	json.Unmarshal(out, &sum)
	m := sum.Result.MRS
	if sum.Result.Behavior != "classical" || !sum.Result.Supported["mrs"] || m == nil {
		t.Fatalf("summary: %s", out)
	}
	if m.Behavior != "domain" || m.Count != 3 || m.Translated != 1 || m.Dropped["DOMAIN-KEYWORD"] != 1 || m.Dropped["ip"] != 1 {
		t.Fatalf("mrs part: %+v", m)
	}

	body, ct = multipartBody(map[string]string{"source": "text", "text": mixedYAML, "mode": "download", "target": "mrs"}, "", "", nil)
	r, file := e.do("POST", "/api/convert", body, map[string]string{"Content-Type": ct})
	if r.StatusCode != 200 || r.Header.Get("X-Rule-Count") != "3" {
		t.Fatalf("download: %d %s", r.StatusCode, file)
	}
	dec, err := rules.DecodeMRS(file)
	if err != nil || dec.Behavior != rules.BehaviorDomain || len(dec.Domains) != 3 {
		t.Fatalf("decoded %+v %v", dec, err)
	}

	// YAML keeps the whole classical list.
	body, ct = multipartBody(map[string]string{"source": "text", "text": mixedYAML, "mode": "download", "target": "yaml"}, "", "", nil)
	_, yaml := e.do("POST", "/api/convert", body, map[string]string{"Content-Type": ct})
	if !strings.Contains(string(yaml), "DOMAIN-KEYWORD,torrent") || !strings.Contains(string(yaml), "IP-CIDR,1.2.3.0/24") {
		t.Fatalf("yaml lost rules:\n%s", yaml)
	}
}

func TestMixedRulesetGetsMRS(t *testing.T) {
	e := newEnv(t, "/")
	e.login("/")
	resp, body := e.do("POST", "/api/rulesets", map[string]any{
		"name": "Смешанный", "slug": "mixed", "behavior": "auto", "formats": []string{"mrs", "list"},
		"include": []string{"DOMAIN-SUFFIX,ua", "DOMAIN-KEYWORD,torrent", "1.2.3.0/24", "+.lib.social"},
	}, nil)
	if resp.StatusCode != 201 {
		t.Fatalf("create: %d %s", resp.StatusCode, body)
	}
	var rs struct{ ID string }
	json.Unmarshal(body, &rs)
	var got struct {
		State struct {
			Behavior string
			Outputs  map[string]struct {
				Error    string
				Behavior string
				Count    int
				Dropped  map[string]int
			}
		}
	}
	json.Unmarshal(e.waitBuilt("/api", rs.ID), &got)
	o := got.State.Outputs["mrs"]
	if got.State.Behavior != "classical" || o.Error != "" || o.Behavior != "domain" || o.Count != 2 || o.Dropped["ip"] != 1 {
		t.Fatalf("state: %+v", got.State)
	}
	_, file := e.do("GET", "/r/mixed.mrs", nil, nil)
	if dec, err := rules.DecodeMRS(file); err != nil || len(dec.Domains) != 2 {
		t.Fatalf("published MRS: %+v %v", dec, err)
	}
	_, list := e.do("GET", "/r/mixed.list", nil, nil)
	if !strings.Contains(string(list), "DOMAIN-KEYWORD,torrent") {
		t.Fatalf("list lost rules: %s", list)
	}
}
