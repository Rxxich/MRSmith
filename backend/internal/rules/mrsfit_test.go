package rules

import (
	"errors"
	"slices"
	"testing"
)

func TestRegexToDomain(t *testing.T) {
	for in, want := range map[string]string{
		`^example\.com$`:            "example.com",
		`^(.*\.)?example\.com$`:     "+.example.com",
		`^(?:.+\.)?Example\.COM$`:   "+.example.com",
		`(^|\.)lib\.social$`:        "+.lib.social",
		`^.+\.example\.com$`:        ".example.com",
		`^[^.]+\.example\.com$`:     "*.example.com",
		`^([^.]+\.)*my-site\.ua$`:   "+.my-site.ua",
		`^cdn\-1\.example\.com$`:    "cdn-1.example.com",
		`example\.com$`:             "",
		`^example\.com`:             "",
		`^example.com$`:             "",
		`^(www|cdn)\.example\.com$`: "",
		`^ad[0-9]+\.example\.com$`:  "",
		`^.*ads.*$`:                 "",
		`^(.*\.)?example\.com\$`:    "",
	} {
		got, ok := RegexToDomain(in)
		if want == "" {
			if ok {
				t.Errorf("%s: translated to %q, want refusal", in, got)
			}
			continue
		}
		if !ok || got != want {
			t.Errorf("%s: got %q %v, want %q", in, got, ok, want)
		}
	}
}

func mixedSet(t *testing.T, text string) *Set {
	t.Helper()
	pr, err := Parse([]byte(text), ParseOptions{Format: InputAuto})
	if err != nil {
		t.Fatal(err)
	}
	return pr.Set
}

func TestResolveMRSFromMixedList(t *testing.T) {
	set := mixedSet(t, `payload:
  - DOMAIN-SUFFIX,ua
  - DOMAIN-SUFFIX,lib.social
  - DOMAIN,example.org
  - DOMAIN-KEYWORD,torrent
  - DOMAIN-REGEX,^(.*\.)?riotgames\.com$
  - DOMAIN-REGEX,^ad[0-9]+\.x\.com$
  - IP-CIDR,1.2.3.0/24,no-resolve
  - PROCESS-NAME,telegram.exe
`)
	if b := Resolve(set, BehaviorAuto, RenderOptions{}); b.Behavior != BehaviorClassical {
		t.Fatalf("mixed list resolved to %s", b.Behavior)
	}
	b, err := ResolveMRS(set, BehaviorAuto, RenderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if b.Behavior != BehaviorDomain || b.Translated != 1 {
		t.Fatalf("behavior %s, translated %d", b.Behavior, b.Translated)
	}
	if want := []string{"example.org", "+.lib.social", "+.riotgames.com", "+.ua"}; !slices.Equal(b.Domains, want) {
		t.Fatalf("domains %v", b.Domains)
	}
	want := map[string]int{DroppedIPs: 1, "DOMAIN-KEYWORD": 1, "DOMAIN-REGEX": 1, "PROCESS-NAME": 1}
	if len(b.Dropped) != len(want) || b.Skipped != 4 {
		t.Fatalf("dropped %v, skipped %d", b.Dropped, b.Skipped)
	}
	for k, n := range want {
		if b.Dropped[k] != n {
			t.Fatalf("dropped %v", b.Dropped)
		}
	}
	data, err := b.Render(OutMRS)
	if err != nil {
		t.Fatal(err)
	}
	m, err := DecodeMRS(data)
	if err != nil || m.Behavior != BehaviorDomain || len(m.Domains) != 4 {
		t.Fatalf("decoded %+v %v", m, err)
	}

	ip, err := ResolveMRS(set, BehaviorIPCIDR, RenderOptions{})
	if err != nil || ip.Behavior != BehaviorIPCIDR || len(ip.Prefixes) != 1 || ip.Dropped[DroppedDomains] != 3 {
		t.Fatalf("ip part: %+v %v", ip, err)
	}
	if _, err := ResolveMRS(mixedSet(t, "DOMAIN-KEYWORD,x\nGEOIP,RU\n"), BehaviorAuto, RenderOptions{}); !errors.Is(err, ErrNothingForMRS) {
		t.Fatalf("keyword-only list: %v", err)
	}
}

func TestResolveMRSPrefersLargerPart(t *testing.T) {
	b, err := ResolveMRS(mixedSet(t, "1.1.1.0/24\n8.8.8.8\nexample.com\n"), BehaviorAuto, RenderOptions{})
	if err != nil || b.Behavior != BehaviorIPCIDR || b.Dropped[DroppedDomains] != 1 {
		t.Fatalf("%+v %v", b, err)
	}
}
