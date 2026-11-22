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
	// NotModified: the client already had this version (304).
	NotModified bool `json:"notModified,omitempty"`
}

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

func count(notModified bool, at time.Time) store.Counter {
	if notModified {
		return store.Counter{Checks: 1, LastAt: at}
	}
	return store.Counter{Downloads: 1, LastAt: at}
}

// Ruleset records a fetch of /r/{slug}.{format}.
func (r *Recorder) Ruleset(id, slug string, f rules.OutputFormat, ip, ua string, notModified bool) {
	now := time.Now().UTC()
	r.mu.Lock()
	defer r.mu.Unlock()
	m := r.rsDelta[id]
	if m == nil {
		m = map[rules.OutputFormat]store.Counter{}
		r.rsDelta[id] = m
	}
	c := m[f]
	c.Add(count(notModified, now))
	m[f] = c
	r.addHit(Hit{Time: now, IP: ip, UserAgent: clean(ua), Kind: "ruleset", RulesetID: id, Name: slug + "." + string(f), NotModified: notModified})
}

// File records a fetch of /f/{bucket}/{key}.
func (r *Recorder) File(bucket, key, ip, ua string, notModified bool) {
	now := time.Now().UTC()
	r.mu.Lock()
	defer r.mu.Unlock()
	k := storage.ObjectKey(bucket, key)
	c := r.stDelta[k]
	c.Add(count(notModified, now))
	r.stDelta[k] = c
	r.addHit(Hit{Time: now, IP: ip, UserAgent: clean(ua), Kind: "file", Bucket: bucket, Key: key, Name: bucket + "/" + key, NotModified: notModified})
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

// Recent returns the latest hits, newest first.
func (r *Recorder) Recent(limit int) ([]Hit, error) {
	out := []Hit{}
	err := r.db.ScanReverse(store.Access, func(_ string, raw []byte) error {
		var h Hit
		if err := json.Unmarshal(raw, &h); err != nil {
			return nil
		}
		out = append(out, h)
		if len(out) >= limit {
			return store.ErrStop
		}
		return nil
	})
	return out, err
}

// Client groups the journal by address and app.
type Client struct {
	IP        string    `json:"ip"`
	UserAgent string    `json:"userAgent"`
	Downloads int       `json:"downloads"`
	Checks    int       `json:"checks"`
	FirstAt   time.Time `json:"firstAt"`
	LastAt    time.Time `json:"lastAt"`
	Files     []string  `json:"files"`
	MoreFiles int       `json:"moreFiles"`
	LastFile  string    `json:"lastFile"`
}

type Summary struct {
	Clients []Client  `json:"clients"`
	Hits    int       `json:"hits"`
	Since   time.Time `json:"since,omitzero"`
	Keep    int       `json:"keep"`
}

// Clients summarises the whole journal, most recently active first.
func (r *Recorder) Clients(limit int) (*Summary, error) {
	type acc struct {
		Client
		files map[string]bool
	}
	by := map[string]*acc{}
	sum := &Summary{Clients: []Client{}, Keep: keepHits}
	err := r.db.ScanReverse(store.Access, func(_ string, raw []byte) error {
		var h Hit
		if err := json.Unmarshal(raw, &h); err != nil {
			return nil
		}
		sum.Hits++
		sum.Since = h.Time
		k := h.IP + "\x00" + h.UserAgent
		a := by[k]
		if a == nil {
			a = &acc{Client: Client{IP: h.IP, UserAgent: h.UserAgent, LastAt: h.Time, LastFile: h.Name}, files: map[string]bool{}}
			by[k] = a
		}
		a.FirstAt = h.Time
		if h.NotModified {
			a.Checks++
		} else {
			a.Downloads++
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
	for _, a := range by {
		sort.Strings(a.Files)
		sum.Clients = append(sum.Clients, a.Client)
	}
	sort.Slice(sum.Clients, func(i, j int) bool { return sum.Clients[i].LastAt.After(sum.Clients[j].LastAt) })
	if len(sum.Clients) > limit {
		sum.Clients = sum.Clients[:limit]
	}
	return sum, nil
}

// Clear empties the journal. Counters on files stay.
func (r *Recorder) Clear() error {
	r.Flush()
	_, err := r.db.DeletePrefix(store.Access, "")
	return err
}
