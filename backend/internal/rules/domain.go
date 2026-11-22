package rules

import (
	"strings"
	"unicode/utf8"

	"golang.org/x/net/idna"
)

// Domain rules use mihomo's domain-behavior syntax:
//
//	example.com     exact match
//	+.example.com   example.com and every subdomain
//	.example.com    subdomains only
//	*.example.com   exactly one label in place of "*"
const (
	prefixSuffix    = "+."
	prefixSubdomain = "."
	maxDomainLen    = 253
	maxLabelLen     = 63
)

var idnaProfile = idna.New(idna.MapForLookup(), idna.Transitional(false), idna.StrictDomainName(false))

// NormalizeDomainRule validates a domain-behavior rule and returns it in
// canonical form (lowercase, punycode, no trailing dot).
func NormalizeDomainRule(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", false
	}
	prefix := ""
	switch {
	case strings.HasPrefix(s, prefixSuffix):
		prefix, s = prefixSuffix, s[2:]
	case strings.HasPrefix(s, prefixSubdomain):
		prefix, s = prefixSubdomain, s[1:]
	}
	base, ok := normalizeHost(s, true)
	if !ok {
		return "", false
	}
	return prefix + base, true
}

// normalizeHost validates a bare host name. When allowWildcard is set, a label
// may be a single "*".
func normalizeHost(s string, allowWildcard bool) (string, bool) {
	s = strings.TrimSuffix(strings.TrimSpace(s), ".")
	if s == "" || len(s) > 1024 {
		return "", false
	}
	if !isASCII(s) {
		if !utf8.ValidString(s) {
			return "", false
		}
		converted, err := idnaProfile.ToASCII(s)
		if err != nil {
			return "", false
		}
		s = converted
	}
	s = strings.ToLower(s)
	if len(s) > maxDomainLen {
		return "", false
	}
	labels := strings.Split(s, ".")
	for _, l := range labels {
		if l == "" || len(l) > maxLabelLen {
			return "", false
		}
		if l == "*" {
			if !allowWildcard {
				return "", false
			}
			continue
		}
		for i := 0; i < len(l); i++ {
			c := l[i]
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
				return "", false
			}
		}
	}
	// A purely numeric last label is never a real TLD; it is most likely a
	// mangled IP address.
	if last := labels[len(labels)-1]; last != "*" && isDigits(last) {
		return "", false
	}
	return s, true
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}

func isDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return s != ""
}

// domainBase strips the rule prefix and returns the host part.
func domainBase(rule string) string {
	if strings.HasPrefix(rule, prefixSuffix) {
		return rule[2:]
	}
	if strings.HasPrefix(rule, prefixSubdomain) {
		return rule[1:]
	}
	return rule
}

// domainToClassical renders a domain rule as a classical rule line.
func domainToClassical(rule string) string {
	base := domainBase(rule)
	hasWildcard := strings.Contains(base, "*")
	switch {
	case !hasWildcard && strings.HasPrefix(rule, prefixSuffix):
		return "DOMAIN-SUFFIX," + base
	case !hasWildcard && strings.HasPrefix(rule, prefixSubdomain):
		// mihomo's DOMAIN-WILDCARD "*" matches any run of characters, so
		// "*.example.com" means "any subdomain", exactly like ".example.com".
		return "DOMAIN-WILDCARD,*." + base
	case !hasWildcard:
		return "DOMAIN," + base
	}
	var b strings.Builder
	b.WriteString("DOMAIN-REGEX,^")
	switch {
	case strings.HasPrefix(rule, prefixSuffix):
		b.WriteString(`(?:.+\.)?`)
	case strings.HasPrefix(rule, prefixSubdomain):
		b.WriteString(`.+\.`)
	}
	for i, label := range strings.Split(base, ".") {
		if i > 0 {
			b.WriteString(`\.`)
		}
		if label == "*" {
			b.WriteString(`[^.]+`)
		} else {
			// labels only contain [a-z0-9_-], none of which are regex metacharacters
			b.WriteString(label)
		}
	}
	b.WriteString("$")
	return b.String()
}
