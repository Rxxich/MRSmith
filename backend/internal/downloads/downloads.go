// Package downloads counts fetches of published files and remembers who
// made them. Hits are buffered in memory and written in batches, so a burst
// of requests does not turn into a burst of disk writes.
package downloads

import (
	"context"
	"encoding/json"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"mrsmith/internal/rules"
	"mrsmith/internal/rulesets"
	"mrsmith/internal/storage"
	"mrsmith/internal/store"
)

const (
	keepHits   = 10000
	maxPending = 5000
	flushEvery = 5 * time.Second
	maxUA      = 200
)

// What became of a request.
const (
	OutcomeOK          = "ok"           // the whole file was sent
	OutcomeNotModified = "not-modified" // the client already had this version (304)
	OutcomeAborted     = "aborted"      // the transfer broke off
	OutcomeBadToken    = "bad-token"    // private set, missing or wrong token
	OutcomeMissing     = "missing"      // no such file (not built, format not published)
	OutcomePrivate     = "private"      // the bucket is not public
	OutcomeError       = "error"        // the server failed
)

// Hit is one request for a published file.
type Hit struct {
	Time      time.Time `json:"time"`
	IP        string    `json:"ip"`
	UserAgent string    `json:"userAgent"`
	Kind      string    `json:"kind"` // ruleset | file
	// RulesetID or Bucket+Key identify the file; Name is how it is shown.
	RulesetID string `json:"rulesetId,omitempty"`
	Bucket    string `json:"bucket,omitempty"`
	Key       string `json:"key,omitempty"`
	Name      string `json:"name"`
	// NotModified is how hits before 1.0.20 marked a 304.
	NotModified bool   `json:"notModified,omitempty"`
	Outcome     string `json:"outcome,omitempty"`
	Bytes       int64  `json:"bytes,omitempty"` // body bytes sent
	Size        int64  `json:"size,omitempty"`  // the whole file
}

// Result is how a request ended, as the server saw it.
type Result struct {
	Outcome     string
	Bytes, Size int64
}

func (h Hit) outcome() string {
	switch {
	case h.Outcome != "":
		return h.Outcome
	case h.NotModified:
		return OutcomeNotModified
	}
	return OutcomeOK
}

// Failed reports outcomes where the client got no file.
func Failed(outcome string) bool { return outcome != OutcomeOK && outcome != OutcomeNotModified }

type Recorder struct {
	db  *store.DB
	rs  *rulesets.Manager
	st  *storage.Service
	log *slog.Logger

	mu      sync.Mutex
	hits    []Hit
	rsDelta map[string]map[rules.OutputFormat]store.Counter
	stDelta map[string]store.Counter
	flushMu sync.Mutex
}

func New(db *store.DB, rs *rulesets.Manager, st *storage.Service, log *slog.Logger) *Recorder {
	return &Recorder{db: db, rs: rs, st: st, log: log,
		rsDelta: map[string]map[rules.OutputFormat]store.Counter{}, stDelta: map[string]store.Counter{}}
}

// count is what a request adds to a file's counter: only a whole file sent
// or a confirmed up-to-date copy count.
func count(outcome string, at time.Time) (store.Counter, bool) {
	switch outcome {
	case OutcomeOK:
		return store.Counter{Downloads: 1, LastAt: at}, true
	case OutcomeNotModified:
		return store.Counter{Checks: 1, LastAt: at}, true
	}
	return store.Counter{}, false
}

// Ruleset records a request for /r/{slug}.{format}.
func (r *Recorder) Ruleset(id, slug string, f rules.OutputFormat, ip, ua string, res Result) {
	now := time.Now().UTC()
	r.mu.Lock()
	defer r.mu.Unlock()
	if d, ok := count(res.Outcome, now); ok {
		m := r.rsDelta[id]
		if m == nil {
			m = map[rules.OutputFormat]store.Counter{}
			r.rsDelta[id] = m
		}
		c := m[f]
		c.Add(d)
		m[f] = c
	}
	r.addHit(Hit{Time: now, IP: ip, UserAgent: clean(ua), Kind: "ruleset", RulesetID: id, Name: slug + "." + string(f),
		Outcome: res.Outcome, Bytes: res.Bytes, Size: res.Size})
}

// File records a request for /f/{bucket}/{key}.
func (r *Recorder) File(bucket, key, ip, ua string, res Result) {
	now := time.Now().UTC()
	r.mu.Lock()
	defer r.mu.Unlock()
	if d, ok := count(res.Outcome, now); ok {
		k := storage.ObjectKey(bucket, key)
		c := r.stDelta[k]
		c.Add(d)
		r.stDelta[k] = c
	}
	r.addHit(Hit{Time: now, IP: ip, UserAgent: clean(ua), Kind: "file", Bucket: bucket, Key: key, Name: bucket + "/" + key,
		Outcome: res.Outcome, Bytes: res.Bytes, Size: res.Size})
}

func (r *Recorder) addHit(h Hit) {
	// Counters stay exact; only the journal drops entries under a flood.
	if len(r.hits) < maxPending {
		r.hits = append(r.hits, h)
	}
}

func clean(ua string) string {
	ua = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, ua)
	ua = strings.ToValidUTF8(strings.TrimSpace(ua), "")
	if utf8.RuneCountInString(ua) > maxUA {
		ua = string([]rune(ua)[:maxUA])
	}
	return ua
}

// Flush writes buffered counts and hits.
func (r *Recorder) Flush() {
	r.flushMu.Lock()
	defer r.flushMu.Unlock()
	r.mu.Lock()
	hits, rsDelta, stDelta := r.hits, r.rsDelta, r.stDelta
	r.hits = nil
	r.rsDelta = map[string]map[rules.OutputFormat]store.Counter{}
	r.stDelta = map[string]store.Counter{}
	r.mu.Unlock()

	if len(rsDelta) > 0 {
		if err := r.rs.AddDownloads(rsDelta); err != nil {
			r.log.Error("save rule set download counts", "err", err)
		}
	}
	if len(stDelta) > 0 {
		if err := r.st.AddDownloads(stDelta); err != nil {
			r.log.Error("save file download counts", "err", err)
		}
	}
	if len(hits) > 0 {
		items := make(map[string]any, len(hits))
		for _, h := range hits {
			items[store.TimeKey(h.Time)] = h
		}
		if err := r.db.PutMany(store.Access, items); err != nil {
			r.log.Error("save download journal", "err", err)
			return
		}
		_ = r.db.TrimOldest(store.Access, keepHits)
	}
}

// Run flushes periodically and once more when ctx ends.
func (r *Recorder) Run(ctx context.Context) {
	t := time.NewTicker(flushEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			r.Flush()
			return
		case <-t.C:
			r.Flush()
		}
	}
}

// Client groups the journal by address and app.
type Client struct {
	IP        string    `json:"ip"`
	UserAgent string    `json:"userAgent"`
	Downloads int       `json:"downloads"`
	Checks    int       `json:"checks"`
	Failed    int       `json:"failed"`
	FirstAt   time.Time `json:"firstAt"`
	LastAt    time.Time `json:"lastAt"`
	Files     []string  `json:"files"`
	MoreFiles int       `json:"moreFiles"`
	LastFile  string    `json:"lastFile"`
}

// Totals describe the whole journal, whatever is filtered.
type Totals struct {
	Hits      int       `json:"hits"`
	Downloads int       `json:"downloads"`
	Checks    int       `json:"checks"`
	Failed    int       `json:"failed"`
	Clients   int       `json:"clients"`
	Since     time.Time `json:"since,omitzero"`
	Keep      int       `json:"keep"`
}

// Filter picks a page of the journal.
type Filter struct {
	View    string // "recent" lists requests, anything else groups them by client
	Query   string // part of an IP, an app or a file name
	Outcome string // "" for all, ok, not-modified or failed
	Offset  int
	Limit   int
}

type Page struct {
	Totals Totals `json:"totals"`
	// Total counts what matches the filter: requests or clients.
	Total   int      `json:"total"`
	Recent  []Hit    `json:"recent"`
	Clients []Client `json:"clients"`
}

func (f Filter) match(h Hit, o string) bool {
	switch f.Outcome {
	case "":
	case "failed":
		if !Failed(o) {
			return false
		}
	default:
		if o != f.Outcome {
			return false
		}
	}
	if f.Query == "" {
		return true
	}
	return strings.Contains(h.IP, f.Query) || strings.Contains(strings.ToLower(h.UserAgent), f.Query) ||
		strings.Contains(strings.ToLower(h.Name), f.Query)
}

// Query reads the journal, newest first, and returns one page of it.
func (r *Recorder) Query(f Filter) (*Page, error) {
	f.Query = strings.ToLower(strings.TrimSpace(f.Query))
	if f.Limit <= 0 {
		f.Limit = 50
	}
	f.Offset = max(f.Offset, 0)
	type acc struct {
		Client
		files map[string]bool
	}
	by := map[string]*acc{}
	everyone := map[string]bool{}
	page := &Page{Totals: Totals{Keep: keepHits}, Recent: []Hit{}, Clients: []Client{}}
	t := &page.Totals
	err := r.db.ScanReverse(store.Access, func(_ string, raw []byte) error {
		var h Hit
		if err := json.Unmarshal(raw, &h); err != nil {
			return nil
		}
		o := h.outcome()
		h.Outcome, h.NotModified = o, false
		t.Hits++
		t.Since = h.Time
		key := h.IP + "\x00" + h.UserAgent
		everyone[key] = true
		switch {
		case o == OutcomeOK:
			t.Downloads++
		case o == OutcomeNotModified:
			t.Checks++
		default:
			t.Failed++
		}
		if !f.match(h, o) {
			return nil
		}
		if f.View == "recent" {
			if page.Total >= f.Offset && page.Total < f.Offset+f.Limit {
				page.Recent = append(page.Recent, h)
			}
			page.Total++
			return nil
		}
		a := by[key]
		if a == nil {
			a = &acc{Client: Client{IP: h.IP, UserAgent: h.UserAgent, LastAt: h.Time, LastFile: h.Name}, files: map[string]bool{}}
			by[key] = a
		}
		a.FirstAt = h.Time
		switch {
		case o == OutcomeOK:
			a.Downloads++
		case o == OutcomeNotModified:
			a.Checks++
		default:
			a.Failed++
		}
		if !a.files[h.Name] {
			a.files[h.Name] = true
			if len(a.Files) < 8 {
				a.Files = append(a.Files, h.Name)
			} else {
				a.MoreFiles++
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	t.Clients = len(everyone)
	if f.View != "recent" {
		all := make([]Client, 0, len(by))
		for _, a := range by {
			sort.Strings(a.Files)
			all = append(all, a.Client)
		}
		sort.Slice(all, func(i, j int) bool { return all[i].LastAt.After(all[j].LastAt) })
		page.Total = len(all)
		if f.Offset < len(all) {
			page.Clients = all[f.Offset:min(f.Offset+f.Limit, len(all))]
		}
	}
	return page, nil
}

// Clear empties the journal. Counters on files stay.
func (r *Recorder) Clear() error {
	r.Flush()
	_, err := r.db.DeletePrefix(store.Access, "")
	return err
}
