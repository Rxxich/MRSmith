package rules

import (
	"errors"
	"strings"
)

// Kinds of rules that do not fit a behavior, as counted in Built.Dropped.
// Classical rules are counted under their own type (DOMAIN-KEYWORD, GEOIP…).
const (
	DroppedDomains = "domains"
	DroppedIPs     = "ip"
)

// ErrNothingForMRS means a list has neither domains nor IP networks.
var ErrNothingForMRS = errors.New("в списке нет ни доменов, ни IP-сетей — MRS из него не собрать")

// dropped counts, by kind, the rules of s that projecting onto b leaves out.
func dropped(s *Set, b Behavior) map[string]int {
	out := map[string]int{}
	switch b {
	case BehaviorDomain:
		if n := len(s.CIDRs); n > 0 {
			out[DroppedIPs] = n
		}
	case BehaviorIPCIDR:
		if n := len(s.Domains); n > 0 {
			out[DroppedDomains] = n
		}
	default:
		return nil
	}
	for k := range s.Classical {
		out[classicalType(k)]++
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func classicalType(rule string) string {
	if i := strings.IndexByte(rule, ','); i > 0 {
		return rule[:i]
	}
	return rule
}

// ResolveMRS takes from a set what an MRS file can hold. MRS stores either
// domains or IP networks; with behavior auto (or classical) it takes the
// larger of the two. DOMAIN-REGEX rules that only describe a domain, its
// subdomains or one level below it are turned into domain rules. Everything
// else is counted in Dropped.
func ResolveMRS(s *Set, behavior Behavior, opt RenderOptions) (*Built, error) {
	work := s
	translated := 0
	if len(s.Classical) > 0 && behavior != BehaviorIPCIDR {
		work = s.Clone()
		for k := range s.Classical {
			if classicalType(k) != "DOMAIN-REGEX" {
				continue
			}
			rule, ok := RegexToDomain(strings.TrimPrefix(k, "DOMAIN-REGEX,"))
			if !ok {
				continue
			}
			if n, ok := NormalizeDomainRule(rule); ok {
				delete(work.Classical, k)
				if _, dup := work.Domains[n]; !dup {
					work.Domains[n] = struct{}{}
				}
				translated++
			}
		}
	}
	if behavior != BehaviorDomain && behavior != BehaviorIPCIDR {
		switch {
		case len(work.Domains) == 0 && len(work.CIDRs) == 0:
			return nil, ErrNothingForMRS
		case len(work.Domains) >= len(work.CIDRs):
			behavior = BehaviorDomain
		default:
			behavior = BehaviorIPCIDR
		}
	}
	b := Resolve(work, behavior, opt)
	if b.Count() == 0 {
		if behavior == BehaviorDomain {
			return nil, errors.New("в списке нет доменов, которые можно записать в MRS")
		}
		return nil, errors.New("в списке нет IP-сетей, которые можно записать в MRS")
	}
	b.Translated = translated
	return b, nil
}

// regexPrefixes map the start of an anchored DOMAIN-REGEX to a mihomo domain
// prefix. anchored means the regex must also start with ^.
var regexPrefixes = []struct {
	re       string
	rule     string
	anchored bool
}{
	{`(.*\.)?`, "+.", true}, {`(.+\.)?`, "+.", true}, {`(?:.*\.)?`, "+.", true}, {`(?:.+\.)?`, "+.", true},
	{`([^.]+\.)*`, "+.", true}, {`(?:[^.]+\.)*`, "+.", true},
	{`(^|\.)`, "+.", false}, {`(?:^|\.)`, "+.", false},
	{`.*\.`, ".", true}, {`.+\.`, ".", true}, {`([^.]+\.)+`, ".", true}, {`(?:[^.]+\.)+`, ".", true},
	{`[^.]+\.`, "*.", true}, {`[^\.]+\.`, "*.", true}, {`[a-z0-9-]+\.`, "*.", true}, {`[\w-]+\.`, "*.", true},
}

// RegexToDomain turns a DOMAIN-REGEX that matches exactly a domain, the
// domain with its subdomains, only subdomains, or one level of subdomains
// into the equivalent mihomo domain rule:
//
//	^example\.com$            → example.com
//	^(.*\.)?example\.com$     → +.example.com
//	(^|\.)example\.com$       → +.example.com
//	^.+\.example\.com$        → .example.com
//	^[^.]+\.example\.com$     → *.example.com
//
// Anything more involved is reported as not translatable.
func RegexToDomain(re string) (string, bool) {
	s := strings.TrimSpace(re)
	start := strings.HasPrefix(s, "^")
	s = strings.TrimPrefix(s, "^")
	if !strings.HasSuffix(s, "$") || strings.HasSuffix(s, `\$`) {
		return "", false
	}
	s = strings.TrimSuffix(s, "$")
	prefix, matched := "", false
	for _, p := range regexPrefixes {
		if strings.HasPrefix(s, p.re) {
			if p.anchored && !start {
				return "", false
			}
			prefix, s, matched = p.rule, s[len(p.re):], true
			break
		}
	}
	if !matched && !start {
		// Without ^ the regex also matches longer names (badexample.com).
		return "", false
	}
	if strings.Contains(strings.ReplaceAll(s, `\.`, ""), ".") {
		// An unescaped dot matches any character: not an exact name.
		return "", false
	}
	host := strings.NewReplacer(`\.`, ".", `\-`, "-").Replace(s)
	if host == "" || strings.ContainsAny(host, `\^$*+?()[]{}|`) {
		return "", false
	}
	return prefix + strings.ToLower(host), true
}
