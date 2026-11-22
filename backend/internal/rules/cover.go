package rules

import (
	"errors"
	"net/netip"
	"strings"
)

// DomainCoverers lists the broader rules that would match every host the
// given domain rule matches. A set containing any of them makes the rule
// redundant. The rule must be in canonical form.
func DomainCoverers(rule string) []string {
	base := domainBase(rule)
	var out []string
	switch {
	case strings.HasPrefix(rule, prefixSuffix):
		// only parents can cover "+.x"
	case strings.HasPrefix(rule, "*."):
		rest := base[2:]
		if strings.Contains(rest, "*") {
			return nil
		}
		out = append(out, prefixSuffix+rest, prefixSubdomain+rest)
		base = rest
	case strings.Contains(base, "*"):
		return nil
	default: // "x" and ".x"
		out = append(out, prefixSuffix+base)
	}
	for i := 0; i < len(base); i++ {
		if base[i] == '.' {
			parent := base[i+1:]
			out = append(out, prefixSuffix+parent, prefixSubdomain+parent)
		}
	}
	return out
}

// Supernets lists every strictly larger network containing p, narrowest first.
func Supernets(p netip.Prefix) []netip.Prefix {
	out := make([]netip.Prefix, 0, p.Bits())
	for bits := p.Bits() - 1; bits >= 0; bits-- {
		s, err := p.Addr().Prefix(bits)
		if err == nil {
			out = append(out, s)
		}
	}
	return out
}

var ErrWouldEmpty = errors.New("после удаления в файле не останется правил")

// Rewrite deletes exact rules from a rule file and renders it back in its
// own format and notation: MRS stays MRS, a classical list stays classical,
// YAML stays YAML. Comments and ordering are not preserved.
func Rewrite(data []byte, remove []string) ([]byte, int, error) {
	pr, err := Parse(data, ParseOptions{})
	if err != nil {
		return nil, 0, err
	}
	rm, _ := ParseEntries(remove)
	n := 0
	for d := range rm.Domains {
		if _, ok := pr.Set.Domains[d]; ok {
			delete(pr.Set.Domains, d)
			n++
		}
	}
	for p := range rm.CIDRs {
		if _, ok := pr.Set.CIDRs[p]; ok {
			delete(pr.Set.CIDRs, p)
			n++
		}
	}
	for c := range rm.Classical {
		if _, ok := pr.Set.Classical[c]; ok {
			delete(pr.Set.Classical, c)
			n++
		}
	}
	if n == 0 {
		return data, 0, nil
	}
	if pr.Set.Len() == 0 {
		return nil, 0, ErrWouldEmpty
	}
	opt := RenderOptions{NoResolve: pr.NoResolve}
	if pr.Format == InputMRS {
		out, err := Resolve(pr.Set, pr.Behavior, opt).Render(OutMRS)
		return out, n, err
	}
	behavior := BehaviorAuto
	if pr.ClassicalStyle {
		behavior = BehaviorClassical
	}
	lines := Resolve(pr.Set, behavior, opt).Lines()
	if pr.Format == InputYAML {
		return renderYAML(lines), n, nil
	}
	return joinLines(lines), n, nil
}
