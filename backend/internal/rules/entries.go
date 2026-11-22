package rules

import "strings"

// NormalizeEntries turns user-typed entries into canonical strings: a domain
// rule ("+.example.com"), a CIDR ("1.2.3.0/24") or a classical line. One
// input can yield several entries (a hosts line with many names).
func NormalizeEntries(in []string) (ok []string, bad []string) {
	seen := map[string]bool{}
	for _, raw := range in {
		e := strings.TrimSpace(raw)
		if e == "" {
			continue
		}
		set := NewSet()
		if !addRule(set, e, ParseOptions{}) {
			bad = append(bad, e)
			continue
		}
		for _, d := range set.SortedDomains() {
			if !seen[d] {
				seen[d] = true
				ok = append(ok, d)
			}
		}
		for _, p := range set.SortedPrefixes(false) {
			if s := p.String(); !seen[s] {
				seen[s] = true
				ok = append(ok, s)
			}
		}
		for _, c := range set.SortedClassical() {
			if !seen[c] {
				seen[c] = true
				ok = append(ok, c)
			}
		}
	}
	return ok, bad
}
