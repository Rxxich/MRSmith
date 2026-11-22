package rules

import (
	"encoding/json"
	"net/netip"
	"sort"
	"strings"

	"go4.org/netipx"
)

// ExcludeMode says what an exclusion takes away besides the rule itself.
type ExcludeMode string

const (
	// ExcludeExact removes only the rule itself.
	ExcludeExact ExcludeMode = "exact"
	// ExcludeTree removes the rule and every narrower rule it covers:
	// "+.example.com" also takes "link.example.com"; 10.0.0.0/8 also takes 10.1.0.0/16.
	ExcludeTree ExcludeMode = "tree"
	// ExcludeParents removes the rule and every broader rule covering it:
	// "link.example.com" also takes "+.example.com"; 10.1.0.0/16 also takes 10.0.0.0/8.
	ExcludeParents ExcludeMode = "parents"
	// ExcludeCut makes the addresses stop matching where a rule set can
	// express it: an IP range is cut out of larger networks, "+.x" minus "x"
	// becomes ".x". A subdomain cannot be cut out of "+.parent".
	ExcludeCut ExcludeMode = "cut"
)

func (m ExcludeMode) Valid() bool {
	switch m {
	case ExcludeExact, ExcludeTree, ExcludeParents, ExcludeCut:
		return true
	}
	return false
}

// Exclusion is one manual removal.
type Exclusion struct {
	Rule string      `json:"rule"`
	Mode ExcludeMode `json:"mode"`
}

// UnmarshalJSON also accepts the plain strings stored by 1.0.0–1.0.2,
// which always meant "cut".
func (e *Exclusion) UnmarshalJSON(b []byte) error {
	if len(b) > 0 && b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*e = Exclusion{Rule: s, Mode: ExcludeCut}
		return nil
	}
	type plain Exclusion
	return json.Unmarshal(b, (*plain)(e))
}

// StillMatched notes an exclusion whose addresses are still matched by a
// broader rule that stays in the set.
type StillMatched struct {
	Rule string `json:"rule"`
	By   string `json:"by"`
}

// ApplyExclusions removes rules according to each exclusion's mode. Rules
// must be in canonical form (see NormalizeEntries). It reports exclusions
// whose addresses are still matched through a broader rule.
func (s *Set) ApplyExclusions(items []Exclusion) []StillMatched {
	treeDom := map[string]bool{}
	var treeIP netipx.IPSetBuilder
	hasTreeIP := false
	var cutIP netipx.IPSetBuilder
	hasCutIP := false
	type conv struct{ from, to string }
	var convs []conv

	parsed := make([]struct {
		ex  Exclusion
		set *Set
	}, 0, len(items))
	for _, it := range items {
		one := NewSet()
		if !addRule(one, it.Rule, ParseOptions{}) {
			continue
		}
		parsed = append(parsed, struct {
			ex  Exclusion
			set *Set
		}{it, one})
		for d := range one.Domains {
			switch it.Mode {
			case ExcludeExact:
				delete(s.Domains, d)
			case ExcludeTree:
				treeDom[d] = true
			case ExcludeParents:
				delete(s.Domains, d)
				for _, c := range DomainCoverers(d) {
					delete(s.Domains, c)
				}
			default: // cut
				base := domainBase(d)
				switch {
				case strings.HasPrefix(d, prefixSuffix):
					treeDom[d] = true
				case strings.HasPrefix(d, prefixSubdomain):
					treeDom[d] = true
					convs = append(convs, conv{prefixSuffix + base, base}) // "+.x" minus subdomains = "x"
				default:
					delete(s.Domains, d)
					convs = append(convs, conv{prefixSuffix + base, prefixSubdomain + base}) // "+.x" minus "x" = ".x"
				}
			}
		}
		for p := range one.CIDRs {
			switch it.Mode {
			case ExcludeExact:
				delete(s.CIDRs, p)
			case ExcludeTree:
				treeIP.AddPrefix(p)
				hasTreeIP = true
			case ExcludeParents:
				delete(s.CIDRs, p)
				for bits := p.Bits() - 1; bits >= 0; bits-- {
					if sup, err := p.Addr().Prefix(bits); err == nil {
						delete(s.CIDRs, sup)
					}
				}
			default:
				cutIP.AddPrefix(p)
				hasCutIP = true
			}
		}
		for c := range one.Classical {
			delete(s.Classical, c)
		}
	}

	// One pass removes every rule under a "tree" root.
	if len(treeDom) > 0 {
		for r := range s.Domains {
			if treeDom[r] {
				delete(s.Domains, r)
				continue
			}
			for _, c := range DomainCoverers(r) {
				if treeDom[c] {
					delete(s.Domains, r)
					break
				}
			}
		}
	}
	for _, c := range convs {
		if _, ok := s.Domains[c.from]; ok {
			delete(s.Domains, c.from)
			s.Domains[c.to] = struct{}{}
		}
	}
	if hasTreeIP {
		if roots, err := treeIP.IPSet(); err == nil {
			for p := range s.CIDRs {
				if roots.ContainsPrefix(p) {
					delete(s.CIDRs, p)
				}
			}
		}
	}
	if hasCutIP && len(s.CIDRs) > 0 {
		if cut, err := cutIP.IPSet(); err == nil {
			for p := range s.CIDRs {
				if !cut.OverlapsPrefix(p) {
					continue
				}
				delete(s.CIDRs, p)
				var b netipx.IPSetBuilder
				b.AddPrefix(p)
				b.RemoveSet(cut)
				if rest, err := b.IPSet(); err == nil {
					for _, q := range rest.Prefixes() {
						s.CIDRs[q] = struct{}{}
					}
				}
			}
		}
	}

	// Which exclusions are still matched through a broader rule?
	var still []StillMatched
	for _, pe := range parsed {
		if pe.ex.Mode == ExcludeParents {
			continue
		}
		for d := range pe.set.Domains {
			for _, c := range DomainCoverers(d) {
				if _, ok := s.Domains[c]; ok {
					still = append(still, StillMatched{Rule: d, By: c})
					break
				}
			}
		}
		if pe.ex.Mode == ExcludeCut {
			continue
		}
		for p := range pe.set.CIDRs {
			if by, ok := coveringPrefix(s, p); ok {
				still = append(still, StillMatched{Rule: p.String(), By: by.String()})
			}
		}
	}
	sort.Slice(still, func(i, j int) bool { return still[i].Rule < still[j].Rule })
	return still
}

func coveringPrefix(s *Set, p netip.Prefix) (netip.Prefix, bool) {
	for bits := p.Bits() - 1; bits >= 0; bits-- {
		sup, err := p.Addr().Prefix(bits)
		if err != nil {
			break
		}
		if _, ok := s.CIDRs[sup]; ok {
			return sup, true
		}
	}
	return netip.Prefix{}, false
}

// Exclude applies plain exclusions in "cut" mode and returns the rules that
// still match through a broader rule.
func (s *Set) Exclude(ex *Set) []string {
	var items []Exclusion
	for d := range ex.Domains {
		items = append(items, Exclusion{Rule: d, Mode: ExcludeCut})
	}
	for p := range ex.CIDRs {
		items = append(items, Exclusion{Rule: p.String(), Mode: ExcludeCut})
	}
	for c := range ex.Classical {
		items = append(items, Exclusion{Rule: c, Mode: ExcludeCut})
	}
	var out []string
	for _, st := range s.ApplyExclusions(items) {
		out = append(out, st.Rule)
	}
	return out
}

// ExclusionPreview describes what one exclusion would change.
type ExclusionPreview struct {
	Removed []string      `json:"removed"`
	Added   []string      `json:"added"`
	Still   *StillMatched `json:"still,omitempty"`
}

// PreviewExclusion applies one exclusion to a copy of the set and reports the
// rules it would remove or replace.
func (s *Set) PreviewExclusion(e Exclusion) ExclusionPreview {
	after := s.Clone()
	still := after.ApplyExclusions([]Exclusion{e})
	removed, added := lineDiff(s.Canonical(), after.Canonical())
	p := ExclusionPreview{Removed: removed, Added: added}
	if len(still) > 0 {
		p.Still = &still[0]
	}
	return p
}

// Canonical lists every rule in its native notation, without aggregation.
func (s *Set) Canonical() []string {
	out := s.SortedDomains()
	for _, p := range s.SortedPrefixes(false) {
		out = append(out, p.String())
	}
	return append(out, s.SortedClassical()...)
}

func lineDiff(old, cur []string) (removed, added []string) {
	o := make(map[string]bool, len(old))
	for _, l := range old {
		o[l] = true
	}
	c := make(map[string]bool, len(cur))
	for _, l := range cur {
		c[l] = true
		if !o[l] {
			added = append(added, l)
		}
	}
	for _, l := range old {
		if !c[l] {
			removed = append(removed, l)
		}
	}
	if removed == nil {
		removed = []string{}
	}
	if added == nil {
		added = []string{}
	}
	return removed, added
}
