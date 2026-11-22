package rules

import (
	"bytes"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"
)

// OutputFormat is a file the panel can produce.
type OutputFormat string

const (
	OutMRS  OutputFormat = "mrs"
	OutYAML OutputFormat = "yaml"
	OutTXT  OutputFormat = "txt"
	OutList OutputFormat = "list"
)

var AllOutputs = []OutputFormat{OutMRS, OutYAML, OutTXT, OutList}

func (f OutputFormat) Valid() bool { return slices.Contains(AllOutputs, f) }

func (f OutputFormat) ContentType() string {
	switch f {
	case OutMRS:
		return "application/octet-stream"
	case OutYAML:
		return "text/yaml; charset=utf-8"
	}
	return "text/plain; charset=utf-8"
}

// ProviderFields returns the behavior/format pair mihomo needs to read the
// produced file.
func (f OutputFormat) ProviderFields(b Behavior) (behavior, format string) {
	switch f {
	case OutMRS:
		return string(b), "mrs"
	case OutYAML:
		return string(b), "yaml"
	case OutList:
		return string(BehaviorClassical), "text"
	}
	return string(b), "text"
}

// Supports reports whether a behavior can be written in a format.
func (f OutputFormat) Supports(b Behavior) bool {
	return f != OutMRS || b == BehaviorDomain || b == BehaviorIPCIDR
}

type RenderOptions struct {
	AggregateIP bool
	NoResolve   bool // append ",no-resolve" to IP rules in classical output
}

// Built is a rule set resolved to one behavior, ready for rendering.
type Built struct {
	Behavior  Behavior
	Domains   []string
	Prefixes  []netip.Prefix
	Classical []string
	Skipped   int
	// Dropped counts skipped rules by kind: DroppedDomains, DroppedIPs or a
	// classical rule type.
	Dropped map[string]int
	// Translated counts DOMAIN-REGEX rules rewritten as domain rules.
	Translated int
	opt        RenderOptions
}

func (b *Built) Count() int { return len(b.Domains) + len(b.Prefixes) + len(b.Classical) }

// Resolve projects a set onto a behavior. BehaviorAuto picks the narrowest
// behavior that fits every rule.
func Resolve(s *Set, behavior Behavior, opt RenderOptions) *Built {
	if behavior == "" || behavior == BehaviorAuto {
		behavior = s.DetectBehavior()
	}
	p, skipped := s.Project(behavior)
	return &Built{
		Behavior:  behavior,
		Domains:   p.SortedDomains(),
		Prefixes:  p.SortedPrefixes(opt.AggregateIP),
		Classical: p.SortedClassical(),
		Skipped:   skipped,
		Dropped:   dropped(s, behavior),
		opt:       opt,
	}
}

// Lines returns the rules in the behavior's native text form (the txt output).
func (b *Built) Lines() []string {
	if b.Behavior == BehaviorClassical {
		return b.ClassicalLines()
	}
	out := make([]string, 0, b.Count())
	out = append(out, b.Domains...)
	for _, p := range b.Prefixes {
		out = append(out, p.String())
	}
	return out
}

// ClassicalLines renders every rule as "TYPE,value" (the list output).
func (b *Built) ClassicalLines() []string {
	out := make([]string, 0, b.Count())
	for _, d := range b.Domains {
		out = append(out, domainToClassical(d))
	}
	for _, p := range b.Prefixes {
		t := "IP-CIDR,"
		if p.Addr().Is6() {
			t = "IP-CIDR6,"
		}
		line := t + p.String()
		if b.opt.NoResolve {
			line += ",no-resolve"
		}
		out = append(out, line)
	}
	out = append(out, b.Classical...)
	return out
}

var ErrUnsupportedFormat = errors.New("формат не поддерживает этот behavior")

// Render produces one output file. MRS output is decoded again and compared
// with the input before it is returned, so a broken file is never published.
func (b *Built) Render(f OutputFormat) ([]byte, error) {
	if !f.Supports(b.Behavior) {
		return nil, fmt.Errorf("%w: MRS умеет только domain и ipcidr, а набор — %s", ErrUnsupportedFormat, b.Behavior)
	}
	switch f {
	case OutMRS:
		return b.renderMRS()
	case OutYAML:
		return renderYAML(b.Lines()), nil
	case OutList:
		return joinLines(b.ClassicalLines()), nil
	case OutTXT:
		return joinLines(b.Lines()), nil
	}
	return nil, fmt.Errorf("неизвестный формат %q", f)
}

func (b *Built) renderMRS() ([]byte, error) {
	var data []byte
	var err error
	if b.Behavior == BehaviorDomain {
		data, err = EncodeDomainMRS(b.Domains)
	} else {
		data, err = EncodeIPMRS(b.Prefixes)
	}
	if err != nil {
		return nil, err
	}
	if err := b.verifyMRS(data); err != nil {
		return nil, fmt.Errorf("проверка MRS не пройдена: %w", err)
	}
	return data, nil
}

func (b *Built) verifyMRS(data []byte) error {
	m, err := DecodeMRS(data)
	if err != nil {
		return err
	}
	if m.Behavior != b.Behavior {
		return errors.New("behavior в заголовке не совпадает")
	}
	if b.Behavior == BehaviorDomain {
		want := domainKeys(b.Domains)
		got := domainKeys(m.Domains)
		if !slices.Equal(want, got) {
			return fmt.Errorf("после декодирования %d ключей вместо %d", len(got), len(want))
		}
		return nil
	}
	want := NewSet()
	for _, p := range b.Prefixes {
		want.CIDRs[p] = struct{}{}
	}
	got := NewSet()
	for _, p := range m.Prefixes {
		got.CIDRs[p] = struct{}{}
	}
	if !slices.Equal(want.SortedPrefixes(true), got.SortedPrefixes(true)) {
		return errors.New("диапазоны адресов после декодирования отличаются")
	}
	return nil
}

func joinLines(lines []string) []byte {
	var buf bytes.Buffer
	for _, l := range lines {
		buf.WriteString(l)
		buf.WriteByte('\n')
	}
	return buf.Bytes()
}

func renderYAML(lines []string) []byte {
	var buf bytes.Buffer
	buf.WriteString("payload:\n")
	for _, l := range lines {
		buf.WriteString("  - '")
		buf.WriteString(strings.ReplaceAll(l, "'", "''"))
		buf.WriteString("'\n")
	}
	return buf.Bytes()
}
