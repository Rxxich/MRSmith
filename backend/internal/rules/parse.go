package rules

import (
	"bytes"
	"errors"
	"fmt"
	"net/netip"
	"strings"

	"go.yaml.in/yaml/v3"
)

// InputFormat is the on-disk format of a rule file.
type InputFormat string

const (
	InputAuto InputFormat = "auto"
	InputMRS  InputFormat = "mrs"
	InputYAML InputFormat = "yaml"
	InputText InputFormat = "text"
)

func (f InputFormat) Valid() bool {
	switch f {
	case InputAuto, InputMRS, InputYAML, InputText:
		return true
	}
	return false
}

type ParseOptions struct {
	Format InputFormat
	// BareAsSuffix treats plain "example.com" lines as "+.example.com".
	// Many community lists mean "domain and subdomains" by a bare name, while
	// mihomo itself reads it as an exact match.
	BareAsSuffix bool
}

type ParseResult struct {
	Format       InputFormat `json:"format"`
	Behavior     Behavior    `json:"behavior"`
	Lines        int         `json:"lines"`
	InvalidCount int         `json:"invalidCount"`
	Invalid      []string    `json:"invalid"`
	Set          *Set        `json:"-"`
	// ClassicalStyle: most lines used "TYPE,value" notation.
	ClassicalStyle bool `json:"classicalStyle"`
	// NoResolve: some IP rules carried ",no-resolve".
	NoResolve  bool `json:"noResolve"`
	commaLines int
}

const maxInvalidSamples = 30

var ErrBinary = errors.New("файл двоичный и не похож на список правил")

// Parse reads a rule file in any supported format.
func Parse(data []byte, opt ParseOptions) (*ParseResult, error) {
	format := opt.Format
	if format == "" || format == InputAuto {
		format = DetectFormat(data)
	}
	res := &ParseResult{Format: format, Set: NewSet()}
	switch format {
	case InputMRS:
		m, err := DecodeMRS(data)
		if err != nil {
			return nil, err
		}
		res.Behavior = m.Behavior
		res.Lines = m.Len()
		for _, d := range m.Domains {
			res.Set.Domains[d] = struct{}{}
		}
		for _, p := range m.Prefixes {
			res.Set.CIDRs[p] = struct{}{}
		}
		return res, nil
	case InputYAML:
		items, err := yamlPayload(data)
		if err != nil {
			// Fall back to line-based reading: it copes with sloppy files
			// where "- item" lines are all we need.
			parseLines(data, opt, res)
		} else {
			for _, it := range items {
				res.add(it, opt)
			}
		}
	default:
		if bytes.IndexByte(head(data, 8192), 0) >= 0 {
			return nil, ErrBinary
		}
		parseLines(data, opt, res)
	}
	res.Behavior = res.Set.DetectBehavior()
	res.ClassicalStyle = res.commaLines*2 > res.Lines
	return res, nil
}

func head(b []byte, n int) []byte {
	if len(b) > n {
		return b[:n]
	}
	return b
}

// DetectFormat guesses the input format from content.
func DetectFormat(data []byte) InputFormat {
	if IsMRS(data) {
		return InputMRS
	}
	for _, line := range bytes.SplitN(head(data, 64*1024), []byte("\n"), 200) {
		l := strings.TrimSpace(string(line))
		if l == "" || l[0] == '#' {
			continue
		}
		if strings.HasPrefix(l, "payload:") || strings.HasPrefix(l, "payload :") {
			return InputYAML
		}
	}
	return InputText
}

func yamlPayload(data []byte) ([]string, error) {
	var doc struct {
		Payload []any `yaml:"payload"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(doc.Payload))
	for _, v := range doc.Payload {
		switch t := v.(type) {
		case string:
			out = append(out, t)
		case nil:
		default:
			// Numbers and other scalars cannot be rules; keep them visible as invalid.
			out = append(out, fmt.Sprint(t))
		}
	}
	return out, nil
}

func parseLines(data []byte, opt ParseOptions, res *ParseResult) {
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	for len(data) > 0 {
		var line []byte
		if i := bytes.IndexByte(data, '\n'); i >= 0 {
			line, data = data[:i], data[i+1:]
		} else {
			line, data = data, nil
		}
		s := cleanLine(string(line))
		if s == "" {
			continue
		}
		// Tolerate YAML list items in text files.
		if strings.HasPrefix(s, "- ") {
			s = unquote(strings.TrimSpace(s[2:]))
		} else if strings.HasPrefix(s, "payload:") {
			continue
		}
		res.add(s, opt)
	}
}

func cleanLine(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	switch s[0] {
	case '#', ';', '!', '[':
		return ""
	}
	if strings.HasPrefix(s, "//") {
		return ""
	}
	for _, sep := range []string{" #", "\t#", " //"} {
		if i := strings.Index(s, sep); i >= 0 {
			s = strings.TrimSpace(s[:i])
		}
	}
	return s
}

func unquote(s string) string {
	if len(s) >= 2 && (s[0] == '\'' && s[len(s)-1] == '\'' || s[0] == '"' && s[len(s)-1] == '"') {
		s = s[1 : len(s)-1]
		s = strings.ReplaceAll(s, "''", "'")
	}
	return s
}

func (r *ParseResult) invalid(s string) {
	r.InvalidCount++
	if len(r.Invalid) < maxInvalidSamples {
		if len(s) > 200 {
			s = s[:200] + "…"
		}
		r.Invalid = append(r.Invalid, s)
	}
}

func (r *ParseResult) add(s string, opt ParseOptions) {
	s = strings.TrimSpace(s)
	if s == "" {
		return
	}
	r.Lines++
	if !addRule(r.Set, s, opt) {
		r.invalid(s)
		return
	}
	if strings.Contains(s, ",") {
		r.commaLines++
		if strings.Contains(strings.ToLower(s), "no-resolve") {
			r.NoResolve = true
		}
	}
}

// ParseEntries parses manual entries typed by a user, one rule per item.
// Bare domains keep mihomo's exact-match meaning.
func ParseEntries(entries []string) (*Set, []string) {
	set := NewSet()
	var bad []string
	for _, e := range entries {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		if !addRule(set, e, ParseOptions{}) {
			bad = append(bad, e)
		}
	}
	return set, bad
}

// addRule recognises one rule in any of the supported notations.
func addRule(set *Set, s string, opt ParseOptions) bool {
	lower := strings.ToLower(s)

	// AdBlock: ||example.com^ and ||example.com^$important
	if strings.HasPrefix(s, "||") {
		rest := s[2:]
		if i := strings.IndexAny(rest, "^$/"); i >= 0 {
			rest = rest[:i]
		}
		return addDomain(set, prefixSuffix+rest)
	}
	if strings.HasPrefix(s, "@@") || strings.HasPrefix(s, "|") {
		return false
	}

	// dnsmasq: server=/a.com/b.com/1.1.1.1, address=/a.com/0.0.0.0, ipset=/a.com/name
	for _, p := range []string{"server=/", "address=/", "ipset=/", "nftset=/", "local=/"} {
		if strings.HasPrefix(lower, p) {
			parts := strings.Split(s[len(p):], "/")
			if len(parts) < 2 {
				return false
			}
			ok := false
			for _, d := range parts[:len(parts)-1] {
				if d != "" && addDomain(set, prefixSuffix+d) {
					ok = true
				}
			}
			return ok
		}
	}

	// v2fly / sing-box style prefixes.
	if i := strings.IndexByte(s, ':'); i > 0 && !strings.Contains(s, ",") {
		key, val := lower[:i], strings.TrimSpace(s[i+1:])
		if at := strings.Index(val, " @"); at >= 0 {
			val = strings.TrimSpace(val[:at])
		} else if at := strings.Index(val, ":@"); at >= 0 {
			val = val[:at]
		}
		switch key {
		case "domain", "domain_suffix":
			return addDomain(set, prefixSuffix+strings.TrimPrefix(val, "."))
		case "full", "domain_full":
			return addDomain(set, val)
		case "keyword", "domain_keyword":
			if val == "" {
				return false
			}
			set.Classical["DOMAIN-KEYWORD,"+strings.ToLower(val)] = struct{}{}
			return true
		case "regexp", "domain_regex":
			if val == "" {
				return false
			}
			set.Classical["DOMAIN-REGEX,"+val] = struct{}{}
			return true
		case "include":
			return false
		}
	}

	if strings.Contains(s, ",") {
		return addClassical(set, s)
	}

	// hosts file: "0.0.0.0 example.com [more.example.com ...]"
	if fields := strings.Fields(s); len(fields) >= 2 {
		if _, err := netip.ParseAddr(fields[0]); err != nil {
			return false
		}
		ok := false
		for _, h := range fields[1:] {
			if isHostsNoise(h) {
				continue
			}
			rule := strings.ToLower(h)
			if opt.BareAsSuffix {
				rule = prefixSuffix + rule
			}
			if addDomain(set, rule) {
				ok = true
			}
		}
		return ok
	}

	if p, ok := parsePrefix(s); ok {
		set.CIDRs[p] = struct{}{}
		return true
	}

	rule := s
	if opt.BareAsSuffix && !strings.HasPrefix(s, prefixSuffix) && !strings.HasPrefix(s, prefixSubdomain) && !strings.HasPrefix(s, "*") {
		rule = prefixSuffix + s
	}
	return addDomain(set, rule)
}

func isHostsNoise(h string) bool {
	switch strings.ToLower(h) {
	case "localhost", "localhost.localdomain", "local", "broadcasthost", "ip6-localhost",
		"ip6-loopback", "ip6-localnet", "ip6-mcastprefix", "ip6-allnodes", "ip6-allrouters",
		"ip6-allhosts", "0.0.0.0":
		return true
	}
	return false
}

func addDomain(set *Set, rule string) bool {
	n, ok := NormalizeDomainRule(rule)
	if !ok {
		return false
	}
	set.Domains[n] = struct{}{}
	return true
}

func parsePrefix(s string) (netip.Prefix, bool) {
	if strings.Contains(s, "/") {
		p, err := netip.ParsePrefix(s)
		if err != nil {
			return netip.Prefix{}, false
		}
		return p.Masked(), true
	}
	a, err := netip.ParseAddr(s)
	if err != nil || a.Zone() != "" {
		return netip.Prefix{}, false
	}
	return netip.PrefixFrom(a, a.BitLen()), true
}

// classicalTypes lists rule types mihomo accepts inside a classical rule-provider.
var classicalTypes = map[string]bool{
	"DOMAIN": true, "DOMAIN-SUFFIX": true, "DOMAIN-KEYWORD": true, "DOMAIN-REGEX": true,
	"DOMAIN-WILDCARD": true, "GEOSITE": true, "GEOIP": true, "IP-CIDR": true, "IP-CIDR6": true,
	"IP-SUFFIX": true, "IP-ASN": true, "SRC-GEOIP": true, "SRC-IP-ASN": true, "SRC-IP-CIDR": true,
	"SRC-IP-SUFFIX": true, "DST-PORT": true, "SRC-PORT": true, "IN-PORT": true, "IN-TYPE": true,
	"IN-USER": true, "IN-NAME": true, "PROCESS-PATH": true, "PROCESS-PATH-REGEX": true,
	"PROCESS-NAME": true, "PROCESS-NAME-REGEX": true, "UID": true, "NETWORK": true, "DSCP": true,
	"AND": true, "OR": true, "NOT": true,
}

func addClassical(set *Set, s string) bool {
	i := strings.IndexByte(s, ',')
	typ := strings.ToUpper(strings.TrimSpace(s[:i]))
	rest := strings.TrimSpace(s[i+1:])
	if rest == "" || !classicalTypes[typ] {
		return false
	}
	// First payload field, without trailing params like "no-resolve" or a policy name.
	payload := rest
	if j := strings.IndexByte(rest, ','); j >= 0 {
		payload = strings.TrimSpace(rest[:j])
	}
	switch typ {
	case "DOMAIN":
		return addDomain(set, strings.TrimPrefix(payload, "."))
	case "DOMAIN-SUFFIX":
		return addDomain(set, prefixSuffix+strings.TrimPrefix(payload, "."))
	case "DOMAIN-WILDCARD":
		if strings.HasPrefix(payload, "*.") && !strings.ContainsAny(payload[2:], "*?") {
			return addDomain(set, prefixSubdomain+payload[2:])
		}
	case "IP-CIDR", "IP-CIDR6":
		if p, ok := parsePrefix(payload); ok {
			set.CIDRs[p] = struct{}{}
			return true
		}
		return false
	case "AND", "OR", "NOT":
		// logic rules carry nested commas; keep the whole remainder
		set.Classical[typ+","+rest] = struct{}{}
		return true
	}
	set.Classical[typ+","+payload+classicalParams(rest)] = struct{}{}
	return true
}

// classicalParams keeps "no-resolve"/"src" flags and drops policy names that
// some lists append (they are not valid inside a rule-provider).
func classicalParams(rest string) string {
	parts := strings.Split(rest, ",")
	var b strings.Builder
	for _, p := range parts[1:] {
		p = strings.TrimSpace(p)
		if p == "no-resolve" || p == "src" {
			b.WriteString(",")
			b.WriteString(p)
		}
	}
	return b.String()
}
