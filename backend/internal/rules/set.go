package rules

import (
	"net/netip"
	"sort"
	"strings"

	"go4.org/netipx"
)

type Behavior string

const (
	BehaviorAuto      Behavior = "auto"
	BehaviorDomain    Behavior = "domain"
	BehaviorIPCIDR    Behavior = "ipcidr"
	BehaviorClassical Behavior = "classical"
)

func (b Behavior) Valid() bool {
	switch b {
	case BehaviorAuto, BehaviorDomain, BehaviorIPCIDR, BehaviorClassical:
		return true
	}
	return false
}

// Set is a deduplicated collection of rules split by kind.
type Set struct {
	Domains   map[string]struct{}
	CIDRs     map[netip.Prefix]struct{}
	Classical map[string]struct{}
}

func NewSet() *Set {
	return &Set{
		Domains:   map[string]struct{}{},
		CIDRs:     map[netip.Prefix]struct{}{},
		Classical: map[string]struct{}{},
	}
}

func (s *Set) Len() int { return len(s.Domains) + len(s.CIDRs) + len(s.Classical) }

func (s *Set) Merge(o *Set) {
	for k := range o.Domains {
		s.Domains[k] = struct{}{}
	}
	for k := range o.CIDRs {
		s.CIDRs[k] = struct{}{}
	}
	for k := range o.Classical {
		s.Classical[k] = struct{}{}
	}
}

func (s *Set) Clone() *Set {
	c := NewSet()
	c.Merge(s)
	return c
}

// DetectBehavior picks the narrowest behavior able to hold every rule.
func (s *Set) DetectBehavior() Behavior {
	switch {
	case len(s.Classical) == 0 && len(s.CIDRs) == 0:
		return BehaviorDomain
	case len(s.Classical) == 0 && len(s.Domains) == 0:
		return BehaviorIPCIDR
	}
	return BehaviorClassical
}

// SortedDomains returns domain rules ordered by host, so related rules sit together.
func (s *Set) SortedDomains() []string {
	out := make([]string, 0, len(s.Domains))
	for k := range s.Domains {
		out = append(out, k)
	}
	key := func(r string) string { return strings.TrimPrefix(domainBase(r), "*.") }
	sort.Slice(out, func(i, j int) bool {
		bi, bj := key(out[i]), key(out[j])
		if bi != bj {
			return bi < bj
		}
		return out[i] < out[j]
	})
	return out
}

// SortedPrefixes returns the CIDRs; with aggregate set, overlapping and
// adjacent networks are merged into the minimal equivalent list.
func (s *Set) SortedPrefixes(aggregate bool) []netip.Prefix {
	if aggregate {
		var b netipx.IPSetBuilder
		for p := range s.CIDRs {
			b.AddPrefix(p)
		}
		ipset, err := b.IPSet()
		if err == nil {
			return ipset.Prefixes()
		}
	}
	out := make([]netip.Prefix, 0, len(s.CIDRs))
	for p := range s.CIDRs {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool {
		if c := out[i].Addr().Compare(out[j].Addr()); c != 0 {
			return c < 0
		}
		return out[i].Bits() < out[j].Bits()
	})
	return out
}

func (s *Set) SortedClassical() []string {
	out := make([]string, 0, len(s.Classical))
	for k := range s.Classical {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Optimize removes domain rules already covered by broader ones. Matching
// behaviour of the set is unchanged.
func (s *Set) Optimize() (removed int) {
	suffix := map[string]bool{} // "+.x"
	subs := map[string]bool{}   // ".x"
	for r := range s.Domains {
		switch {
		case strings.HasPrefix(r, prefixSuffix):
			suffix[r[2:]] = true
		case strings.HasPrefix(r, prefixSubdomain):
			subs[r[1:]] = true
		}
	}
	// strictAncestorCovered reports whether a parent of host already matches
	// all of its subdomains.
	strictAncestorCovered := func(host string) bool {
		for i := 0; i < len(host); i++ {
			if host[i] != '.' {
				continue
			}
			parent := host[i+1:]
			if suffix[parent] || subs[parent] {
				return true
			}
		}
		return false
	}
	for r := range s.Domains {
		base := domainBase(r)
		if strings.Contains(base, "*") {
			// Only a leading "*." label is analysable; anything else stays.
			if !strings.HasPrefix(r, "*.") || strings.Contains(base[2:], "*") {
				continue
			}
			parent := base[2:]
			if suffix[parent] || subs[parent] || strictAncestorCovered(parent) {
				delete(s.Domains, r)
				removed++
			}
			continue
		}
		covered := false
		switch {
		case strings.HasPrefix(r, prefixSuffix):
			covered = strictAncestorCovered(base)
		case strings.HasPrefix(r, prefixSubdomain):
			covered = suffix[base] || strictAncestorCovered(base)
		default:
			covered = suffix[base] || strictAncestorCovered(base)
		}
		if covered {
			delete(s.Domains, r)
			removed++
		}
	}
	// "x" together with ".x" is exactly "+.x".
	for base := range subs {
		if _, ok := s.Domains[prefixSubdomain+base]; !ok {
			continue
		}
		if _, ok := s.Domains[base]; ok {
			delete(s.Domains, prefixSubdomain+base)
			delete(s.Domains, base)
			s.Domains[prefixSuffix+base] = struct{}{}
			removed++
		}
	}
	return removed
}

// Project keeps only the rules a behavior can express. Rules that do not
// fit are counted as skipped.
func (s *Set) Project(b Behavior) (*Set, int) {
	out := NewSet()
	skipped := 0
	switch b {
	case BehaviorDomain:
		for k := range s.Domains {
			out.Domains[k] = struct{}{}
		}
		skipped = len(s.CIDRs) + len(s.Classical)
	case BehaviorIPCIDR:
		for k := range s.CIDRs {
			out.CIDRs[k] = struct{}{}
		}
		skipped = len(s.Domains) + len(s.Classical)
	default:
		out.Merge(s)
	}
	return out, skipped
}
