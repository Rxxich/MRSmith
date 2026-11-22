// Package audit looks across rule sets and stored rule files for rules that
// repeat or are already covered by a broader rule elsewhere, and applies the
// suggested clean-up.
//
// Such rules are dead weight in mihomo: the first matching RULE-SET wins, so
// a duplicate in a later provider never fires, and a rule covered by a
// broader one in the same provider never matters.
package audit

import (
	"bytes"
	"context"
	"fmt"
	"net/netip"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"go4.org/netipx"

	"mrsmith/internal/apperr"
	"mrsmith/internal/rules"
	"mrsmith/internal/rulesets"
	"mrsmith/internal/storage"
	"mrsmith/internal/store"
)

const (
	KindSame    = "same"    // the same rule in several providers
	KindCovered = "covered" // a broader rule in another provider already matches it
	KindInner   = "inner"   // a broader rule in the same provider already matches it

	ProviderRuleset = "ruleset"
	ProviderObject  = "object"

	maxFindings = 50_000
	keptReports = 3
)

var ErrReportGone = apperr.NotFound("результаты аудита устарели — запустите его заново")

// Candidate is something the audit can inspect.
type Candidate struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Name   string `json:"name"`
	Detail string `json:"detail"`
	Rules  int    `json:"rules"`
	Size   int64  `json:"size"`
	System bool   `json:"system"`

	token string // changes whenever the content changes; used for caching
}

// Provider is a candidate after loading.
type Provider struct {
	Candidate
	Behavior rules.Behavior `json:"behavior"`
	Error    string         `json:"error,omitempty"`

	set       *rules.Set
	ips       *netipx.IPSet
	rulesetID string
	bucket    string
	key       string
}

// Occurrence is one place a finding touches.
type Occurrence struct {
	P      int    `json:"p"`
	Rule   string `json:"rule"`
	Remove bool   `json:"remove,omitempty"` // suggested for removal
	Keep   bool   `json:"keep,omitempty"`   // the copy that stays
	Inner  bool   `json:"inner,omitempty"`  // also redundant inside its own provider
	Union  bool   `json:"union,omitempty"`  // covered only by several networks together
}

type Finding struct {
	ID    int          `json:"id"`
	Kind  string       `json:"kind"`
	Value string       `json:"value"`
	Occ   []Occurrence `json:"occ"`
}

// Pair counts what can be removed from one provider because another has it.
type Pair struct {
	From    int `json:"from"`
	To      int `json:"to"`
	Same    int `json:"same"`
	Covered int `json:"covered"`
	Inner   int `json:"inner"`
	Wide    int `json:"wide"` // distinct broader rules in To that cover rules of From
}

type Totals struct {
	Providers int `json:"providers"`
	Rules     int `json:"rules"`
	Findings  int `json:"findings"`
	Same      int `json:"same"`
	Covered   int `json:"covered"`
	Inner     int `json:"inner"`
	Removable int `json:"removable"`
}

type Report struct {
	ID         string     `json:"id"`
	CreatedAt  time.Time  `json:"createdAt"`
	DurationMs int64      `json:"durationMs"`
	Providers  []Provider `json:"providers"`
	Totals     Totals     `json:"totals"`
	Pairs      []Pair     `json:"pairs"`
	Truncated  bool       `json:"truncated"`

	findings []Finding
}

type Service struct {
	rs       *rulesets.Manager
	st       *storage.Service
	maxBytes int64

	cacheMu sync.Mutex
	cache   map[string]cachedProvider

	mu      sync.Mutex
	reports map[string]*Report
	order   []string
	running sync.Mutex
}

func New(rs *rulesets.Manager, st *storage.Service, maxBytes int64) *Service {
	return &Service{rs: rs, st: st, maxBytes: maxBytes, reports: map[string]*Report{}, cache: map[string]cachedProvider{}}
}

type cachedProvider struct {
	token string
	prov  Provider
}

// cachedLoad parses a provider once per content version.
func (s *Service) cachedLoad(c Candidate) Provider {
	s.cacheMu.Lock()
	hit, ok := s.cache[c.ID]
	s.cacheMu.Unlock()
	if ok && c.token != "" && hit.token == c.token {
		p := hit.prov
		p.Candidate.System = c.System
		return p
	}
	p := s.load(c)
	if p.Error == "" && c.token != "" {
		s.cacheMu.Lock()
		s.cache[c.ID] = cachedProvider{token: c.token, prov: p}
		s.cacheMu.Unlock()
	}
	return p
}

// forget drops cached providers that no longer exist.
func (s *Service) forget(alive map[string]bool) {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	for id := range s.cache {
		if !alive[id] {
			delete(s.cache, id)
		}
	}
}

func isRuleFile(key string) bool {
	switch strings.ToLower(path.Ext(key)) {
	case ".mrs", ".yaml", ".yml", ".txt", ".list", ".conf":
		return true
	}
	return false
}

// Candidates lists every rule set and every rule file in storage.
func (s *Service) Candidates() ([]Candidate, error) {
	var out []Candidate
	sets, err := s.rs.List()
	if err != nil {
		return nil, err
	}
	for _, r := range sets {
		out = append(out, Candidate{ID: "rs:" + r.ID, Kind: ProviderRuleset, Name: r.Name, Detail: r.Slug, Rules: r.State.Count, token: r.State.Hash})
	}
	buckets, err := s.st.Buckets()
	if err != nil {
		return nil, err
	}
	for _, b := range buckets {
		objs, err := s.st.Walk(b.Name)
		if err != nil {
			return nil, err
		}
		for _, o := range objs {
			if !isRuleFile(o.Key) || o.Size > s.maxBytes {
				continue
			}
			out = append(out, Candidate{
				ID: "obj:" + o.Bucket + "/" + o.Key, Kind: ProviderObject, Name: path.Base(o.Key),
				Detail: o.Bucket + "/" + o.Key, Size: o.Size, System: b.System, token: o.SHA256,
			})
		}
	}
	if out == nil {
		out = []Candidate{}
	}
	alive := make(map[string]bool, len(out))
	for _, c := range out {
		alive[c.ID] = true
	}
	s.forget(alive)
	return out, nil
}

func (s *Service) load(c Candidate) Provider {
	p := Provider{Candidate: c}
	switch {
	case strings.HasPrefix(c.ID, "rs:"):
		p.rulesetID = strings.TrimPrefix(c.ID, "rs:")
		rs, lines, err := s.rs.Lines(p.rulesetID)
		if err != nil {
			p.Error = err.Error()
			return p
		}
		p.Name, p.Detail = rs.Name, rs.Slug
		pr, err := rules.Parse([]byte(strings.Join(lines, "\n")), rules.ParseOptions{Format: rules.InputText})
		if err != nil {
			p.Error = err.Error()
			return p
		}
		p.set, p.Behavior = pr.Set, rs.State.Behavior
	case strings.HasPrefix(c.ID, "obj:"):
		p.bucket, p.key, _ = strings.Cut(strings.TrimPrefix(c.ID, "obj:"), "/")
		o, err := s.st.Object(p.bucket, p.key)
		if err != nil {
			p.Error = err.Error()
			return p
		}
		data, err := s.st.ReadAll(o, s.maxBytes)
		if err != nil {
			p.Error = err.Error()
			return p
		}
		pr, err := rules.Parse(data, rules.ParseOptions{})
		if err != nil {
			p.Error = err.Error()
			return p
		}
		p.set, p.Behavior = pr.Set, pr.Behavior
	default:
		p.Error = "неизвестный источник"
		return p
	}
	p.Rules = p.set.Len()
	if len(p.set.CIDRs) > 0 {
		var b netipx.IPSetBuilder
		for x := range p.set.CIDRs {
			b.AddPrefix(x)
		}
		p.ips, _ = b.IPSet()
	}
	return p
}

// Run audits the chosen providers (all of them when ids is empty).
func (s *Service) Run(ctx context.Context, ids []string) (*Report, error) {
	if !s.running.TryLock() {
		return nil, apperr.Conflict("аудит уже идёт — дождитесь результата")
	}
	defer s.running.Unlock()
	started := time.Now()

	all, err := s.Candidates()
	if err != nil {
		return nil, err
	}
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	var provs []Provider
	for _, c := range all {
		if len(ids) == 0 && !c.System || want[c.ID] {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			provs = append(provs, s.cachedLoad(c))
		}
	}
	if len(provs) < 1 {
		return nil, apperr.Bad("нечего проверять: выберите хотя бы один набор или файл")
	}

	r := &Report{ID: store.NewID(), CreatedAt: started.UTC(), Providers: provs}
	a := &analysis{provs: provs, report: r, pairs: map[[2]int]*Pair{}, wide: map[[2]int]map[string]bool{}, removable: map[string]bool{}}
	a.run()
	r.DurationMs = time.Since(started).Milliseconds()

	s.mu.Lock()
	s.reports[r.ID] = r
	s.order = append(s.order, r.ID)
	for len(s.order) > keptReports {
		delete(s.reports, s.order[0])
		s.order = s.order[1:]
	}
	s.mu.Unlock()
	return r, nil
}

func (s *Service) Report(id string) (*Report, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.reports[id]
	if !ok {
		return nil, ErrReportGone
	}
	return r, nil
}

// Query filters findings of a report.
type Query struct {
	Kind     string
	Provider int // -1 for any
	Text     string
	Offset   int
	Limit    int
}

func (s *Service) Findings(id string, q Query) (int, []Finding, error) {
	r, err := s.Report(id)
	if err != nil {
		return 0, nil, err
	}
	text := strings.ToLower(strings.TrimSpace(q.Text))
	var matched []Finding
	for _, f := range r.findings {
		if q.Kind != "" && f.Kind != q.Kind {
			continue
		}
		if q.Provider >= 0 && !touches(f, q.Provider) {
			continue
		}
		if text != "" && !strings.Contains(strings.ToLower(f.Value), text) && !occMatches(f, text) {
			continue
		}
		matched = append(matched, f)
	}
	total := len(matched)
	if q.Offset > total {
		q.Offset = total
	}
	end := min(q.Offset+q.Limit, total)
	page := matched[q.Offset:end]
	if page == nil {
		page = []Finding{}
	}
	return total, page, nil
}

func touches(f Finding, p int) bool {
	for _, o := range f.Occ {
		if o.P == p {
			return true
		}
	}
	return false
}

func occMatches(f Finding, text string) bool {
	for _, o := range f.Occ {
		if strings.Contains(strings.ToLower(o.Rule), text) {
			return true
		}
	}
	return false
}

// ---- analysis --------------------------------------------------------------

type analysis struct {
	provs     []Provider
	report    *Report
	pairs     map[[2]int]*Pair
	wide      map[[2]int]map[string]bool
	removable map[string]bool
	innerDom  []map[string]bool
	innerIP   []map[netip.Prefix]bool
}

func (a *analysis) add(f Finding) {
	t := &a.report.Totals
	t.Findings++
	switch f.Kind {
	case KindSame:
		t.Same++
	case KindCovered:
		t.Covered++
	case KindInner:
		t.Inner++
	}
	for _, rm := range f.Occ {
		if !rm.Remove {
			continue
		}
		a.removable[strconv.Itoa(rm.P)+"\x00"+rm.Rule] = true
		seen := map[int]bool{}
		for _, k := range f.Occ {
			if !k.Keep || seen[k.P] {
				continue
			}
			seen[k.P] = true
			key := [2]int{rm.P, k.P}
			pr := a.pairs[key]
			if pr == nil {
				pr = &Pair{From: rm.P, To: k.P}
				a.pairs[key] = pr
			}
			switch f.Kind {
			case KindSame:
				pr.Same++
			case KindCovered:
				pr.Covered++
				if !k.Union {
					if a.wide[key] == nil {
						a.wide[key] = map[string]bool{}
					}
					a.wide[key][k.Rule] = true
				}
			case KindInner:
				pr.Inner++
			}
		}
	}
	if len(a.report.findings) < maxFindings {
		a.report.findings = append(a.report.findings, f)
	} else {
		a.report.Truncated = true
	}
}

func (a *analysis) run() {
	n := len(a.provs)
	a.innerDom = make([]map[string]bool, n)
	a.innerIP = make([]map[netip.Prefix]bool, n)
	domOwners := map[string][]int{}
	clsOwners := map[string][]int{}
	ipOwners := map[netip.Prefix][]int{}
	for p := range a.provs {
		a.innerDom[p] = map[string]bool{}
		a.innerIP[p] = map[netip.Prefix]bool{}
		set := a.provs[p].set
		if set == nil {
			continue
		}
		a.report.Totals.Providers++
		a.report.Totals.Rules += set.Len()
		for d := range set.Domains {
			domOwners[d] = append(domOwners[d], p)
		}
		for c := range set.Classical {
			clsOwners[c] = append(clsOwners[c], p)
		}
		for x := range set.CIDRs {
			ipOwners[x] = append(ipOwners[x], p)
		}
	}

	// Rules covered by a broader rule, inside the provider or elsewhere.
	for p := range a.provs {
		set := a.provs[p].set
		if set == nil {
			continue
		}
		for d := range set.Domains {
			var inner, cross []Occurrence
			seen := map[int]bool{}
			for _, c := range rules.DomainCoverers(d) {
				for _, q := range domOwners[c] {
					if q == p {
						if inner == nil {
							inner = []Occurrence{{P: p, Rule: c, Keep: true}}
						}
					} else if !seen[q] {
						seen[q] = true
						cross = append(cross, Occurrence{P: q, Rule: c, Keep: true})
					}
				}
			}
			if inner != nil {
				a.innerDom[p][d] = true
				a.add(Finding{Kind: KindInner, Value: d, Occ: append([]Occurrence{{P: p, Rule: d, Remove: true}}, inner...)})
			} else if cross != nil {
				a.add(Finding{Kind: KindCovered, Value: d, Occ: append([]Occurrence{{P: p, Rule: d, Remove: true}}, cross...)})
			}
		}
		for x := range set.CIDRs {
			if sup, ok := ownSupernet(set, x); ok {
				a.innerIP[p][x] = true
				a.add(Finding{Kind: KindInner, Value: x.String(), Occ: []Occurrence{{P: p, Rule: x.String(), Remove: true}, {P: p, Rule: sup.String(), Keep: true}}})
				continue
			}
			var cross []Occurrence
			for q := range a.provs {
				other := a.provs[q]
				if q == p || other.ips == nil {
					continue
				}
				if _, exact := other.set.CIDRs[x]; exact || !other.ips.ContainsPrefix(x) {
					continue
				}
				occ := Occurrence{P: q, Rule: "несколько сетей вместе", Keep: true, Union: true}
				if sup, ok := ownSupernet(other.set, x); ok {
					occ.Rule, occ.Union = sup.String(), false
				}
				cross = append(cross, occ)
			}
			if cross != nil {
				a.add(Finding{Kind: KindCovered, Value: x.String(), Occ: append([]Occurrence{{P: p, Rule: x.String(), Remove: true}}, cross...)})
			}
		}
	}

	// The same rule in several providers.
	for d, ps := range domOwners {
		a.same(d, ps, func(p int) bool { return a.innerDom[p][d] })
	}
	for c, ps := range clsOwners {
		a.same(c, ps, func(int) bool { return false })
	}
	for x, ps := range ipOwners {
		a.same(x.String(), ps, func(p int) bool { return a.innerIP[p][x] })
	}

	a.report.Totals.Removable = len(a.removable)
	order := map[string]int{KindSame: 0, KindCovered: 1, KindInner: 2}
	f := a.report.findings
	sort.Slice(f, func(i, j int) bool {
		if f[i].Kind != f[j].Kind {
			return order[f[i].Kind] < order[f[j].Kind]
		}
		if f[i].Value != f[j].Value {
			return f[i].Value < f[j].Value
		}
		return f[i].Occ[0].P < f[j].Occ[0].P
	})
	for i := range f {
		f[i].ID = i
	}
	for key, pr := range a.pairs {
		pr.Wide = len(a.wide[key])
		a.report.Pairs = append(a.report.Pairs, *pr)
	}
	sort.Slice(a.report.Pairs, func(i, j int) bool {
		x, y := a.report.Pairs[i], a.report.Pairs[j]
		if sx, sy := x.Same+x.Covered+x.Inner, y.Same+y.Covered+y.Inner; sx != sy {
			return sx > sy
		}
		return x.From < y.From || x.From == y.From && x.To < y.To
	})
	if a.report.Pairs == nil {
		a.report.Pairs = []Pair{}
	}
}

// same records a rule present in several providers. The copy stays in the
// most specific provider (fewest rules); copies that are already redundant
// inside their own provider are left to the "inner" clean-up.
func (a *analysis) same(rule string, owners []int, inner func(int) bool) {
	if len(owners) < 2 {
		return
	}
	keeper := -1
	for _, p := range owners {
		if inner(p) {
			continue
		}
		if keeper < 0 || a.provs[p].Rules < a.provs[keeper].Rules || a.provs[p].Rules == a.provs[keeper].Rules && p < keeper {
			keeper = p
		}
	}
	if keeper < 0 {
		return
	}
	occ := make([]Occurrence, 0, len(owners))
	removable := false
	for _, p := range owners {
		o := Occurrence{P: p, Rule: rule, Keep: p == keeper, Inner: inner(p)}
		o.Remove = !o.Keep && !o.Inner
		removable = removable || o.Remove
		occ = append(occ, o)
	}
	if !removable {
		return
	}
	sort.Slice(occ, func(i, j int) bool { return occ[i].P < occ[j].P })
	a.add(Finding{Kind: KindSame, Value: rule, Occ: occ})
}

func ownSupernet(set *rules.Set, x netip.Prefix) (netip.Prefix, bool) {
	for bits := x.Bits() - 1; bits >= 0; bits-- {
		s, err := x.Addr().Prefix(bits)
		if err != nil {
			break
		}
		if _, ok := set.CIDRs[s]; ok {
			return s, true
		}
	}
	return netip.Prefix{}, false
}

// ---- fixing ----------------------------------------------------------------

// PairRef selects every finding between two providers. With Broad set, the
// broader rules in To are removed instead of the narrow copies in From.
type PairRef struct {
	From  int  `json:"from"`
	To    int  `json:"to"`
	Broad bool `json:"broad"`
}

type FixRequest struct {
	Findings []int           `json:"findings"`
	Keep     map[string]int  `json:"keep"`  // finding id → provider to keep, for "same" findings
	Broad    map[string]bool `json:"broad"` // finding id → remove the broader rule, for "covered"/"inner"
	Pair     *PairRef        `json:"pair"`
	Inner    []int           `json:"inner"`
}

type FixResult struct {
	Provider string `json:"provider"`
	Action   string `json:"action"` // excluded | rewritten | optimized
	Removed  int    `json:"removed"`
	Error    string `json:"error,omitempty"`
}

type plan struct {
	remove   map[int]map[string]bool
	optimize map[int][2]bool // domains, ips
}

func (pl *plan) drop(p int, rule string) {
	if pl.remove[p] == nil {
		pl.remove[p] = map[string]bool{}
	}
	pl.remove[p][rule] = true
}

// dropBroad removes the broader rules of a finding. A network covered only
// by several smaller networks together has no single rule to remove.
func (pl *plan) dropBroad(f Finding) {
	for _, o := range f.Occ {
		if o.Keep && !o.Union {
			pl.drop(o.P, o.Rule)
		}
	}
}

// Fix applies suggestions. Rule sets get exclusions of exactly the chosen
// rules (visible and reversible under "Ручные правки"); stored files are
// rewritten in their own format.
func (s *Service) Fix(ctx context.Context, id string, req FixRequest) ([]FixResult, error) {
	r, err := s.Report(id)
	if err != nil {
		return nil, err
	}
	pl := &plan{remove: map[int]map[string]bool{}, optimize: map[int][2]bool{}}

	for _, fid := range req.Findings {
		if fid < 0 || fid >= len(r.findings) {
			return nil, apperr.Bad("неизвестная находка %d", fid)
		}
		f := r.findings[fid]
		key := strconv.Itoa(fid)
		switch {
		case f.Kind == KindSame:
			keep, override := req.Keep[key]
			for _, o := range f.Occ {
				remove := o.Remove
				if override {
					remove = o.P != keep && !o.Inner
				}
				if remove {
					pl.drop(o.P, o.Rule)
				}
			}
		case req.Broad[key]:
			pl.dropBroad(f)
		default:
			pl.drop(f.Occ[0].P, f.Occ[0].Rule)
		}
	}

	inner := map[int]bool{}
	for _, p := range req.Inner {
		inner[p] = true
	}
	if pr := req.Pair; pr != nil {
		if pr.From == pr.To {
			inner[pr.From] = true
		} else {
			for _, f := range r.findings {
				if f.Kind == KindInner || !hasKeep(f, pr.To) || !hasRemove(f, pr.From) {
					continue
				}
				if pr.Broad {
					if f.Kind == KindCovered {
						for _, o := range f.Occ {
							if o.P == pr.To && o.Keep && !o.Union {
								pl.drop(o.P, o.Rule)
							}
						}
					}
					continue
				}
				for _, o := range f.Occ {
					if o.P == pr.From && o.Remove {
						pl.drop(o.P, o.Rule)
					}
				}
			}
		}
	}
	// Cleaning a whole provider: a rule set gets optimization switched on,
	// a stored file loses the redundant lines.
	for _, f := range r.findings {
		if f.Kind != KindInner || !inner[f.Occ[0].P] {
			continue
		}
		p := f.Occ[0].P
		if r.Providers[p].Kind == ProviderRuleset {
			o := pl.optimize[p]
			if strings.Contains(f.Value, "/") {
				o[1] = true
			} else {
				o[0] = true
			}
			pl.optimize[p] = o
		} else {
			pl.drop(p, f.Occ[0].Rule)
		}
	}
	if len(pl.remove) == 0 && len(pl.optimize) == 0 {
		return nil, apperr.Bad("нечего исправлять")
	}

	var results []FixResult
	touched := map[int]bool{}
	for p := range pl.remove {
		touched[p] = true
	}
	for p := range pl.optimize {
		touched[p] = true
	}
	ps := make([]int, 0, len(touched))
	for p := range touched {
		ps = append(ps, p)
	}
	sort.Ints(ps)
	for _, p := range ps {
		if p < 0 || p >= len(r.Providers) {
			continue
		}
		var list []string
		for rule := range pl.remove[p] {
			list = append(list, rule)
		}
		sort.Strings(list)
		results = append(results, s.applyTo(ctx, r.Providers[p], list, pl.optimize[p])...)
	}
	// The report no longer matches reality.
	s.mu.Lock()
	delete(s.reports, id)
	s.mu.Unlock()
	return results, nil
}

func hasKeep(f Finding, p int) bool {
	for _, o := range f.Occ {
		if o.P == p && o.Keep {
			return true
		}
	}
	return false
}

func hasRemove(f Finding, p int) bool {
	for _, o := range f.Occ {
		if o.P == p && o.Remove {
			return true
		}
	}
	return false
}

func (s *Service) applyTo(ctx context.Context, prov Provider, list []string, opt [2]bool) []FixResult {
	var out []FixResult
	name := prov.Name
	if prov.Kind == ProviderObject {
		name = prov.Detail
	}
	if prov.Kind == ProviderRuleset {
		if len(list) > 0 {
			res := FixResult{Provider: name, Action: "excluded", Removed: len(list)}
			ex := make([]rules.Exclusion, len(list))
			for i, rule := range list {
				ex[i] = rules.Exclusion{Rule: rule, Mode: rules.ExcludeExact}
			}
			if _, err := s.rs.EditEntries(ctx, prov.rulesetID, nil, ex); err != nil {
				res.Error, res.Removed = err.Error(), 0
			}
			out = append(out, res)
		}
		if opt[0] || opt[1] {
			res := FixResult{Provider: name, Action: "optimized"}
			if _, err := s.rs.EnableOptimizations(ctx, prov.rulesetID, opt[0], opt[1]); err != nil {
				res.Error = err.Error()
			}
			out = append(out, res)
		}
		return out
	}
	res := FixResult{Provider: name, Action: "rewritten"}
	o, err := s.st.Object(prov.bucket, prov.key)
	if err != nil {
		res.Error = err.Error()
		return append(out, res)
	}
	data, err := s.st.ReadAll(o, s.maxBytes)
	if err != nil {
		res.Error = err.Error()
		return append(out, res)
	}
	updated, n, err := rules.Rewrite(data, list)
	if err != nil {
		res.Error = err.Error()
		return append(out, res)
	}
	if n > 0 {
		if _, err := s.st.Put(prov.bucket, prov.key, bytes.NewReader(updated), o.ContentType); err != nil {
			res.Error = fmt.Sprintf("не удалось сохранить: %v", err)
			return append(out, res)
		}
	}
	res.Removed = n
	return append(out, res)
}
