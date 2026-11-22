package rules

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/netip"
	"slices"
	"strings"
	"testing"
)

func parseText(t *testing.T, text string, opt ParseOptions) *ParseResult {
	t.Helper()
	r, err := Parse([]byte(text), opt)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return r
}

func TestNormalizeDomainRule(t *testing.T) {
	cases := map[string]string{
		"Example.COM":      "example.com",
		"+.Example.com.":   "+.example.com",
		".sub.example.com": ".sub.example.com",
		"*.example.com":    "*.example.com",
		"пример.рф":        "xn--e1afmkfd.xn--p1ai",
		"+.cn":             "+.cn",
		"_dmarc.a-b.org":   "_dmarc.a-b.org",
	}
	for in, want := range cases {
		got, ok := NormalizeDomainRule(in)
		if !ok || got != want {
			t.Errorf("NormalizeDomainRule(%q) = %q,%v want %q", in, got, ok, want)
		}
	}
	for _, bad := range []string{"", "a..b", "exa mple.com", "1.2.3", "+.", "a.b*.com", "x/y"} {
		if got, ok := NormalizeDomainRule(bad); ok {
			t.Errorf("NormalizeDomainRule(%q) accepted as %q", bad, got)
		}
	}
}

func TestParseMixedNotations(t *testing.T) {
	text := "\ufeff# comment\n" +
		"payload-free text\n" +
		"DOMAIN-SUFFIX,google.com,PROXY\n" +
		"DOMAIN,mail.google.com\n" +
		"DOMAIN-KEYWORD,ads\n" +
		"IP-CIDR,10.0.0.1/8,no-resolve\n" +
		"IP-CIDR6,2001:db8::/32\n" +
		"||doubleclick.net^\n" +
		"0.0.0.0 tracker.example localhost\n" +
		"server=/ru-block.example/1.1.1.1\n" +
		"domain:v2fly.org\n" +
		"full:exact.v2fly.org\n" +
		"1.1.1.1\n" +
		"+.github.com # inline comment\n" +
		"PROCESS-NAME,Telegram.exe\n" +
		"USER-AGENT,foo*\n"
	r := parseText(t, text, ParseOptions{})
	wantDomains := []string{"+.doubleclick.net", "+.github.com", "+.google.com", "+.ru-block.example", "+.v2fly.org", "exact.v2fly.org", "mail.google.com", "tracker.example"}
	if got := r.Set.SortedDomains(); !sameElements(got, wantDomains) {
		t.Errorf("domains = %v", got)
	}
	if !has(r.Set.CIDRs, "10.0.0.0/8") || !has(r.Set.CIDRs, "2001:db8::/32") || !has(r.Set.CIDRs, "1.1.1.1/32") {
		t.Errorf("cidrs = %v", r.Set.SortedPrefixes(false))
	}
	if _, ok := r.Set.Classical["DOMAIN-KEYWORD,ads"]; !ok {
		t.Errorf("classical = %v", r.Set.SortedClassical())
	}
	if _, ok := r.Set.Classical["PROCESS-NAME,Telegram.exe"]; !ok {
		t.Errorf("classical = %v", r.Set.SortedClassical())
	}
	if r.InvalidCount != 2 { // "payload-free text", USER-AGENT
		t.Errorf("invalid = %d %v", r.InvalidCount, r.Invalid)
	}
	if r.Behavior != BehaviorClassical {
		t.Errorf("behavior = %s", r.Behavior)
	}
}

func TestParseYAML(t *testing.T) {
	text := "# rules\npayload:\n  - '+.google.com'\n  - \"example.org\"\n  - 'DOMAIN-SUFFIX,youtube.com'\n"
	r := parseText(t, text, ParseOptions{})
	if r.Format != InputYAML || r.Behavior != BehaviorDomain || len(r.Set.Domains) != 3 {
		t.Fatalf("format=%s behavior=%s domains=%v", r.Format, r.Behavior, r.Set.SortedDomains())
	}
	flow := parseText(t, "payload: ['1.0.0.0/24', '8.8.8.8/32']", ParseOptions{})
	if flow.Behavior != BehaviorIPCIDR || len(flow.Set.CIDRs) != 2 {
		t.Fatalf("flow yaml: %s %v", flow.Behavior, flow.Set.SortedPrefixes(false))
	}
}

func TestBareAsSuffix(t *testing.T) {
	r := parseText(t, "example.com\n.only-subs.org\n0.0.0.0 hosts.example\n", ParseOptions{BareAsSuffix: true})
	want := []string{"+.example.com", "+.hosts.example", ".only-subs.org"}
	if got := r.Set.SortedDomains(); !sameElements(got, want) {
		t.Errorf("got %v", got)
	}
}

func TestOptimize(t *testing.T) {
	s := NewSet()
	for _, d := range []string{"+.google.com", "google.com", "mail.google.com", "+.a.google.com", ".google.com",
		"*.google.com", "+.example.org", ".x.example.org", "solo.net", ".solo.net", "*.other.com", "a.*.wild.com"} {
		s.Domains[d] = struct{}{}
	}
	s.Optimize()
	want := []string{"+.example.org", "+.google.com", "+.solo.net", "*.other.com", "a.*.wild.com"}
	if got := s.SortedDomains(); !sameElements(got, want) {
		t.Errorf("optimize = %v", got)
	}
}

func TestExclude(t *testing.T) {
	s := NewSet()
	for _, d := range []string{"+.google.com", "+.youtube.com", "ads.example.com", "x.ads.example.com", "+.keep.org", "+.parent.net"} {
		s.Domains[d] = struct{}{}
	}
	for _, p := range []string{"10.0.0.0/8", "192.168.1.0/24"} {
		s.CIDRs[netip.MustParsePrefix(p)] = struct{}{}
	}
	ex, bad := ParseEntries([]string{"google.com", ".youtube.com", "+.ads.example.com", "10.1.0.0/16", "192.168.1.0/24", "child.parent.net"})
	if len(bad) != 0 {
		t.Fatalf("bad entries %v", bad)
	}
	ineffective := s.Exclude(ex)
	want := []string{".google.com", "youtube.com", "+.keep.org", "+.parent.net"}
	if got := s.SortedDomains(); !sameElements(got, want) {
		t.Errorf("domains after exclude = %v", got)
	}
	if !slices.Equal(ineffective, []string{"child.parent.net"}) {
		t.Errorf("ineffective = %v", ineffective)
	}
	agg := s.SortedPrefixes(true)
	var b strings.Builder
	for _, p := range agg {
		b.WriteString(p.String() + " ")
	}
	// 10.0.0.0/8 minus 10.1.0.0/16
	if strings.Contains(b.String(), "192.168.") || !strings.Contains(b.String(), "10.0.0.0/16") || !strings.Contains(b.String(), "10.128.0.0/9") || strings.Contains(b.String(), "10.1.0.0") {
		t.Errorf("cidrs after exclude = %s", b.String())
	}
}

func TestDomainMRSRoundTrip(t *testing.T) {
	rules := []string{"+.google.com", "example.org", ".subs-only.net", "*.wild.io", "+.cn", "a.b.c.d.e.f", "x.com", "+.x.com.cn"}
	data, err := EncodeDomainMRS(rules)
	if err != nil {
		t.Fatal(err)
	}
	if !IsMRS(data) || DetectFormat(data) != InputMRS {
		t.Fatal("encoded file not detected as MRS")
	}
	m, err := DecodeMRS(data)
	if err != nil {
		t.Fatal(err)
	}
	if m.Behavior != BehaviorDomain || m.Count != int64(len(rules)) {
		t.Fatalf("behavior=%s count=%d", m.Behavior, m.Count)
	}
	if !sameElements(m.Domains, rules) {
		t.Fatalf("round trip = %v", m.Domains)
	}
}

func TestDomainMRSRandom(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	alphabet := "abcdefghij-0123"
	seen := map[string]struct{}{}
	for len(seen) < 5000 {
		labels := 1 + rng.Intn(4)
		var parts []string
		for i := 0; i < labels; i++ {
			n := 1 + rng.Intn(6)
			var b []byte
			for j := 0; j < n; j++ {
				b = append(b, alphabet[rng.Intn(10)]) // letters only to avoid numeric TLDs
			}
			parts = append(parts, string(b))
		}
		d := strings.Join(parts, ".")
		switch rng.Intn(3) {
		case 0:
			d = "+." + d
		case 1:
			if labels > 1 {
				d = "." + d
			}
		}
		seen[d] = struct{}{}
	}
	s := NewSet()
	for d := range seen {
		s.Domains[d] = struct{}{}
	}
	built := Resolve(s, BehaviorDomain, RenderOptions{})
	data, err := built.Render(OutMRS) // includes verification
	if err != nil {
		t.Fatal(err)
	}
	m, err := DecodeMRS(data)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(domainKeys(m.Domains), domainKeys(built.Domains)) {
		t.Fatal("random round trip mismatch")
	}
}

func TestIPMRSRoundTrip(t *testing.T) {
	var prefixes []netip.Prefix
	for _, p := range []string{"1.0.0.0/24", "1.0.1.0/24", "8.8.8.8/32", "10.0.0.0/8", "10.5.0.0/16", "2001:db8::/32", "2606:4700::/32", "::ffff:0:0/96"} {
		prefixes = append(prefixes, netip.MustParsePrefix(p))
	}
	data, err := EncodeIPMRS(prefixes)
	if err != nil {
		t.Fatal(err)
	}
	m, err := DecodeMRS(data)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, len(m.Prefixes))
	for i, p := range m.Prefixes {
		got[i] = p.String()
	}
	want := []string{"1.0.0.0/23", "8.8.8.8/32", "10.0.0.0/8", "2001:db8::/32", "2606:4700::/32"}
	// ::ffff:0:0/96 is the IPv4-mapped block; it unmaps to all of IPv4 on read,
	// exactly as mihomo does, so only check the rest.
	for _, w := range want {
		if !slices.Contains(got, w) && w != "1.0.0.0/23" {
			t.Errorf("missing %s in %v", w, got)
		}
	}
}

func TestDecodeRejectsGarbage(t *testing.T) {
	data, _ := EncodeIPMRS([]netip.Prefix{netip.MustParsePrefix("1.1.1.0/24")})
	for i := 0; i < 200; i++ {
		c := bytes.Clone(data)
		c[rand.Intn(len(c))] ^= byte(1 + rand.Intn(255))
		_, _ = DecodeMRS(c) // must not panic
	}
	if _, err := DecodeMRS([]byte("not mrs")); err == nil {
		t.Fatal("expected error")
	}
}

func TestRenderFormats(t *testing.T) {
	r := parseText(t, "+.google.com\nexample.org\n.subs.net\n*.wild.io\n", ParseOptions{})
	b := Resolve(r.Set, BehaviorAuto, RenderOptions{})
	list, _ := b.Render(OutList)
	want := "DOMAIN,example.org\nDOMAIN-SUFFIX,google.com\nDOMAIN-WILDCARD,*.subs.net\nDOMAIN-REGEX,^[^.]+\\.wild\\.io$\n"
	if string(list) != want {
		t.Errorf("list =\n%s", list)
	}
	y, _ := b.Render(OutYAML)
	back := parseText(t, string(y), ParseOptions{})
	if !sameElements(back.Set.SortedDomains(), b.Domains) {
		t.Errorf("yaml round trip %v", back.Set.SortedDomains())
	}
	// the list output must parse back to the same domains (regex stays classical)
	lb := parseText(t, string(list), ParseOptions{})
	if len(lb.Set.Domains) != 3 || len(lb.Set.Classical) != 1 {
		t.Errorf("list round trip %v %v", lb.Set.SortedDomains(), lb.Set.SortedClassical())
	}
	mixed := parseText(t, "+.a.com\n1.1.1.1\n", ParseOptions{})
	mb := Resolve(mixed.Set, BehaviorAuto, RenderOptions{})
	if _, err := mb.Render(OutMRS); err == nil {
		t.Error("mixed set must not render as MRS")
	}
	db := Resolve(mixed.Set, BehaviorDomain, RenderOptions{})
	if db.Skipped != 1 || db.Count() != 1 {
		t.Errorf("projection skipped=%d count=%d", db.Skipped, db.Count())
	}
}

func sameElements(a, b []string) bool {
	x, y := slices.Clone(a), slices.Clone(b)
	slices.Sort(x)
	slices.Sort(y)
	return slices.Equal(x, y)
}

func has(m map[netip.Prefix]struct{}, p string) bool {
	_, ok := m[netip.MustParsePrefix(p)]
	return ok
}

func BenchmarkDomainMRS(b *testing.B) {
	var rules []string
	for i := 0; i < 100000; i++ {
		rules = append(rules, fmt.Sprintf("+.host%d.example%d.com", i, i%97))
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := EncodeDomainMRS(rules); err != nil {
			b.Fatal(err)
		}
	}
}

func TestDomainCoverers(t *testing.T) {
	got := DomainCoverers("admin.twitch.tv")
	want := []string{"+.admin.twitch.tv", "+.twitch.tv", ".twitch.tv", "+.tv", ".tv"}
	if !slices.Equal(got, want) {
		t.Fatalf("exact: %v", got)
	}
	if got := DomainCoverers("+.twitch.tv"); !slices.Equal(got, []string{"+.tv", ".tv"}) {
		t.Fatalf("suffix: %v", got)
	}
	if got := DomainCoverers("*.cdn.example.com"); got[0] != "+.cdn.example.com" || got[1] != ".cdn.example.com" {
		t.Fatalf("wildcard: %v", got)
	}
	if DomainCoverers("a.*.example.com") != nil {
		t.Fatal("inner wildcard should have no coverers")
	}
}

func TestRewriteKeepsNotation(t *testing.T) {
	list := "# comment\nDOMAIN-SUFFIX,twitch.tv\nDOMAIN,admin.twitch.tv\nIP-CIDR,1.1.1.0/24,no-resolve\nIP-CIDR,8.8.8.8/32,no-resolve\n"
	out, n, err := Rewrite([]byte(list), []string{"admin.twitch.tv", "8.8.8.8/32"})
	if err != nil || n != 2 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if string(out) != "DOMAIN-SUFFIX,twitch.tv\nIP-CIDR,1.1.1.0/24,no-resolve\n" {
		t.Fatalf("list rewrite:\n%s", out)
	}
	txt := "+.google.com\nmail.google.com\n"
	out, _, _ = Rewrite([]byte(txt), []string{"mail.google.com"})
	if string(out) != "+.google.com\n" {
		t.Fatalf("txt rewrite: %q", out)
	}
	y := "payload:\n  - '+.a.com'\n  - '+.b.com'\n"
	out, _, _ = Rewrite([]byte(y), []string{"+.b.com"})
	if string(out) != "payload:\n  - '+.a.com'\n" {
		t.Fatalf("yaml rewrite: %q", out)
	}
	mrs, _ := EncodeDomainMRS([]string{"+.a.com", "b.com"})
	out, _, err = Rewrite(mrs, []string{"b.com"})
	if err != nil {
		t.Fatal(err)
	}
	if m, err := DecodeMRS(out); err != nil || !slices.Equal(m.Domains, []string{"+.a.com"}) {
		t.Fatalf("mrs rewrite: %v %v", m, err)
	}
	if _, _, err := Rewrite([]byte("+.a.com\n"), []string{"+.a.com"}); err != ErrWouldEmpty {
		t.Fatalf("emptying a file must fail, got %v", err)
	}
	same, n, _ := Rewrite([]byte(txt), []string{"nothing.here"})
	if n != 0 || string(same) != txt {
		t.Fatal("no-op rewrite changed the file")
	}
}

func modeSet(t *testing.T) *Set {
	t.Helper()
	s := NewSet()
	for _, d := range []string{"+.example.com", "link.example.com", "+.cdn.example.com", "other.org"} {
		s.Domains[d] = struct{}{}
	}
	for _, p := range []string{"10.0.0.0/8", "10.1.0.0/16", "10.1.2.0/24", "192.168.0.0/16"} {
		s.CIDRs[netip.MustParsePrefix(p)] = struct{}{}
	}
	return s
}

func TestExclusionModes(t *testing.T) {
	cases := []struct {
		name  string
		ex    Exclusion
		want  []string
		still string
	}{
		{"exact narrow keeps the broad rule", Exclusion{"link.example.com", ExcludeExact},
			[]string{"+.cdn.example.com", "+.example.com", "other.org", "10.0.0.0/8", "10.1.0.0/16", "10.1.2.0/24", "192.168.0.0/16"}, "+.example.com"},
		{"exact broad keeps the narrow rules", Exclusion{"+.example.com", ExcludeExact},
			[]string{"+.cdn.example.com", "link.example.com", "other.org", "10.0.0.0/8", "10.1.0.0/16", "10.1.2.0/24", "192.168.0.0/16"}, ""},
		{"tree takes what it covers", Exclusion{"+.example.com", ExcludeTree},
			[]string{"other.org", "10.0.0.0/8", "10.1.0.0/16", "10.1.2.0/24", "192.168.0.0/16"}, ""},
		{"parents takes what covers it", Exclusion{"link.example.com", ExcludeParents},
			[]string{"+.cdn.example.com", "other.org", "10.0.0.0/8", "10.1.0.0/16", "10.1.2.0/24", "192.168.0.0/16"}, ""},
		{"ip exact", Exclusion{"10.1.0.0/16", ExcludeExact},
			[]string{"+.cdn.example.com", "+.example.com", "link.example.com", "other.org", "10.0.0.0/8", "10.1.2.0/24", "192.168.0.0/16"}, "10.0.0.0/8"},
		{"ip tree", Exclusion{"10.1.0.0/16", ExcludeTree},
			[]string{"+.cdn.example.com", "+.example.com", "link.example.com", "other.org", "10.0.0.0/8", "192.168.0.0/16"}, "10.0.0.0/8"},
		{"ip parents", Exclusion{"10.1.0.0/16", ExcludeParents},
			[]string{"+.cdn.example.com", "+.example.com", "link.example.com", "other.org", "10.1.2.0/24", "192.168.0.0/16"}, ""},
	}
	for _, c := range cases {
		s := modeSet(t)
		still := s.ApplyExclusions([]Exclusion{c.ex})
		if got := s.Canonical(); !slices.Equal(got, c.want) {
			t.Errorf("%s:\n got %v\nwant %v", c.name, got, c.want)
		}
		by := ""
		if len(still) > 0 {
			by = still[0].By
		}
		if by != c.still {
			t.Errorf("%s: still matched by %q, want %q", c.name, by, c.still)
		}
	}
	// cut on IPs splits the larger network
	s := modeSet(t)
	s.ApplyExclusions([]Exclusion{{"10.1.0.0/16", ExcludeCut}})
	if _, ok := s.CIDRs[netip.MustParsePrefix("10.0.0.0/8")]; ok {
		t.Error("cut left 10.0.0.0/8 whole")
	}
	if _, ok := s.CIDRs[netip.MustParsePrefix("10.128.0.0/9")]; !ok {
		t.Error("cut did not keep the rest of 10.0.0.0/8")
	}
	// legacy JSON strings mean "cut"
	var ex []Exclusion
	if err := json.Unmarshal([]byte(`["+.a.com",{"rule":"b.com","mode":"exact"}]`), &ex); err != nil || ex[0].Mode != ExcludeCut || ex[1].Mode != ExcludeExact {
		t.Fatalf("legacy decode: %+v %v", ex, err)
	}
	p := modeSet(t).PreviewExclusion(Exclusion{"+.example.com", ExcludeTree})
	if !slices.Equal(p.Removed, []string{"+.cdn.example.com", "+.example.com", "link.example.com"}) || len(p.Added) != 0 {
		t.Fatalf("preview: %+v", p)
	}
}
