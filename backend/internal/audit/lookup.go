package audit

import (
	"net/netip"
	"regexp"
	"sort"
	"strings"
	"sync"

	"mrsmith/internal/apperr"
	"mrsmith/internal/rules"
)

const (
	RelMatch  = "match"  // the rule fires for the queried address
	RelInside = "inside" // a narrower rule under the queried domain or network
	RelText   = "text"   // the rule text contains the query
	hitLimit  = 50
)

type LookupMatch struct {
	Rule     string `json:"rule"`
	Relation string `json:"relation"`
}

type LookupHit struct {
	ID      string        `json:"id"`
	Kind    string        `json:"kind"`
	Name    string        `json:"name"`
	Detail  string        `json:"detail"`
	System  bool          `json:"system"`
	Matches []LookupMatch `json:"matches"`
	More    int           `json:"more"`
}

type LookupResult struct {
	Query   string      `json:"query"`
	Mode    string      `json:"mode"` // domain | ip | text
	Checked int         `json:"checked"`
	Hits    []LookupHit `json:"hits"`
}

var (
	regexMu    sync.Mutex
	regexCache = map[string]*regexp.Regexp{}
)

func compiled(expr string) *regexp.Regexp {
	regexMu.Lock()
	defer regexMu.Unlock()
	if re, ok := regexCache[expr]; ok {
		return re
	}
	re, err := regexp.Compile(expr)
	if err != nil || len(regexCache) > 5000 {
		re = nil
	}
	regexCache[expr] = re
	return re
}

// Lookup finds every rule set and stored file that has a rule for the query:
// a host (admin.twitch.tv), a rule (+.twitch.tv), an IP or network, or any
// text that rules contain.
func (s *Service) Lookup(query string) (*LookupResult, error) {
	q := strings.TrimSpace(query)
	if q == "" || len(q) > 300 {
		return nil, apperr.Bad("введите домен, IP или часть имени")
	}
	cands, err := s.Candidates()
	if err != nil {
		return nil, err
	}
	res := &LookupResult{Query: q, Hits: []LookupHit{}}
	var find func(set *rules.Set) []LookupMatch

	if p, ok := parseIP(q); ok {
		res.Mode = "ip"
		find = func(set *rules.Set) []LookupMatch {
			var out []LookupMatch
			for x := range set.CIDRs {
				switch {
				case x.Contains(p.Addr()) && x.Bits() <= p.Bits():
					out = append(out, LookupMatch{x.String(), RelMatch})
				case p.Contains(x.Addr()) && p.Bits() < x.Bits():
					out = append(out, LookupMatch{x.String(), RelInside})
				}
			}
			return out
		}
	} else if norm, ok := rules.NormalizeDomainRule(q); ok && !strings.Contains(norm, "*") &&
		(strings.Contains(strings.Trim(norm, "+."), ".") || strings.HasPrefix(q, "+.") || strings.HasPrefix(q, ".")) {
		// A single word is searched as text; "+.cn" asks for the domain explicitly.
		res.Mode = "domain"
		host := strings.TrimPrefix(strings.TrimPrefix(norm, "+."), ".")
		firing := hostRules(host)
		find = func(set *rules.Set) []LookupMatch {
			var out []LookupMatch
			for _, r := range firing {
				if _, ok := set.Domains[r]; ok {
					out = append(out, LookupMatch{r, RelMatch})
				}
			}
			for r := range set.Domains {
				base := strings.TrimPrefix(strings.TrimPrefix(r, "+."), ".")
				if strings.HasSuffix(base, "."+host) || r == "."+host {
					out = append(out, LookupMatch{r, RelInside})
				}
			}
			for c := range set.Classical {
				typ, val, _ := strings.Cut(c, ",")
				val, _, _ = strings.Cut(val, ",")
				switch typ {
				case "DOMAIN-KEYWORD":
					if val != "" && strings.Contains(host, strings.ToLower(val)) {
						out = append(out, LookupMatch{c, RelMatch})
					}
				case "DOMAIN-REGEX":
					if re := compiled(val); re != nil && re.MatchString(host) {
						out = append(out, LookupMatch{c, RelMatch})
					}
				}
			}
			return out
		}
	} else {
		res.Mode = "text"
		needle := strings.ToLower(q)
		find = func(set *rules.Set) []LookupMatch {
			var out []LookupMatch
			for _, r := range set.Canonical() {
				if strings.Contains(strings.ToLower(r), needle) {
					out = append(out, LookupMatch{r, RelText})
				}
			}
			return out
		}
	}

	for _, c := range cands {
		p := s.cachedLoad(c)
		if p.set == nil {
			continue
		}
		res.Checked++
		matches := find(p.set)
		if len(matches) == 0 {
			continue
		}
		sort.Slice(matches, func(i, j int) bool {
			if matches[i].Relation != matches[j].Relation {
				return matches[i].Relation == RelMatch
			}
			return matches[i].Rule < matches[j].Rule
		})
		hit := LookupHit{ID: c.ID, Kind: c.Kind, Name: p.Name, Detail: p.Detail, System: c.System, Matches: matches}
		if len(hit.Matches) > hitLimit {
			hit.More = len(hit.Matches) - hitLimit
			hit.Matches = hit.Matches[:hitLimit]
		}
		res.Hits = append(res.Hits, hit)
	}
	sort.SliceStable(res.Hits, func(i, j int) bool {
		a, b := res.Hits[i], res.Hits[j]
		if a.System != b.System {
			return !a.System
		}
		am, bm := a.Matches[0].Relation == RelMatch, b.Matches[0].Relation == RelMatch
		if am != bm {
			return am
		}
		return a.Kind == ProviderRuleset && b.Kind != ProviderRuleset
	})
	return res, nil
}

func parseIP(q string) (netip.Prefix, bool) {
	if p, err := netip.ParsePrefix(q); err == nil {
		return p.Masked(), true
	}
	if a, err := netip.ParseAddr(q); err == nil && a.Zone() == "" {
		a = a.Unmap()
		return netip.PrefixFrom(a, a.BitLen()), true
	}
	return netip.Prefix{}, false
}

// hostRules lists the domain rules that fire for a host.
func hostRules(host string) []string {
	out := []string{host, "+." + host}
	labels := strings.Split(host, ".")
	for i := 1; i < len(labels); i++ {
		parent := strings.Join(labels[i:], ".")
		out = append(out, "+."+parent, "."+parent)
		if i == 1 {
			out = append(out, "*."+parent)
		}
	}
	return out
}
