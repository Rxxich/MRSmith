package rulesets

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"mrsmith/internal/fetch"
	"mrsmith/internal/rules"
	"mrsmith/internal/storage"
	"mrsmith/internal/store"
)

type Manager struct {
	db       *store.DB
	storage  *storage.Service
	fetcher  *fetch.Client
	dir      string
	maxBytes int64
	log      *slog.Logger

	mu      sync.Mutex
	locks   map[string]*sync.Mutex
	running map[string]bool
	sem     chan struct{}

	cacheMu sync.Mutex
	cache   map[string]cachedLines

	onChange func(rs *Ruleset, ev *Event)
}

// OnChange registers fn to run after a rebuild from sources (by schedule or
// by the refresh button) changed the content of a set with notices on.
// Call it before Run.
func (m *Manager) OnChange(fn func(rs *Ruleset, ev *Event)) { m.onChange = fn }

type cachedLines struct {
	hash  string
	lines []string
}

func NewManager(db *store.DB, st *storage.Service, f *fetch.Client, dataDir string, maxBytes int64, log *slog.Logger) (*Manager, error) {
	for _, d := range []string{"outputs", "state"} {
		if err := os.MkdirAll(filepath.Join(dataDir, d), 0o700); err != nil {
			return nil, err
		}
	}
	if err := st.EnsureManagedBucket(storage.DownloadsBucket); err != nil {
		return nil, err
	}
	if err := st.EnsureSystemBucket(storage.SourcesBucket); err != nil {
		return nil, err
	}
	return &Manager{
		db: db, storage: st, fetcher: f, dir: dataDir, maxBytes: maxBytes, log: log,
		locks: map[string]*sync.Mutex{}, running: map[string]bool{},
		sem:   make(chan struct{}, 2),
		cache: map[string]cachedLines{},
	}, nil
}

func (m *Manager) lock(id string) *sync.Mutex {
	m.mu.Lock()
	defer m.mu.Unlock()
	l, ok := m.locks[id]
	if !ok {
		l = &sync.Mutex{}
		m.locks[id] = l
	}
	return l
}

// ---- CRUD ------------------------------------------------------------------

func (m *Manager) List() ([]Ruleset, error) {
	list, err := store.List[Ruleset](m.db, store.Rulesets, "")
	if err != nil {
		return nil, err
	}
	sort.Slice(list, func(i, j int) bool { return list[i].CreatedAt.After(list[j].CreatedAt) })
	return list, nil
}

func (m *Manager) Get(id string) (*Ruleset, error) {
	var rs Ruleset
	if err := m.db.Get(store.Rulesets, id, &rs); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &rs, nil
}

func (m *Manager) BySlug(slug string) (*Ruleset, error) {
	list, err := m.List()
	if err != nil {
		return nil, err
	}
	for i := range list {
		if list[i].Slug == slug {
			return &list[i], nil
		}
	}
	return nil, ErrNotFound
}

func (m *Manager) slugTaken(slug, exceptID string) bool {
	rs, err := m.BySlug(slug)
	return err == nil && rs.ID != exceptID
}

func (m *Manager) Create(in Input) (*Ruleset, error) {
	if err := in.normalize(); err != nil {
		return nil, err
	}
	if m.slugTaken(in.Slug, "") {
		return nil, invalid("ссылка /r/%s уже занята другим набором", in.Slug)
	}
	now := time.Now().UTC()
	rs := &Ruleset{ID: store.NewID(), CreatedAt: now, State: State{Status: StatusPending}}
	apply(rs, in, nil)
	if rs.Private {
		rs.Token = store.RandomHex(16)
	}
	if err := m.db.Put(store.Rulesets, rs.ID, rs); err != nil {
		return nil, err
	}
	return rs, nil
}

func (m *Manager) Update(id string, in Input) (*Ruleset, error) {
	if err := in.normalize(); err != nil {
		return nil, err
	}
	if m.slugTaken(in.Slug, id) {
		return nil, invalid("ссылка /r/%s уже занята другим набором", in.Slug)
	}
	l := m.lock(id)
	l.Lock()
	defer l.Unlock()
	var dropped []string
	var old []Source
	rs, err := store.Modify(m.db, store.Rulesets, id, func(rs *Ruleset) error {
		old = slices.Clone(rs.Sources)
		dropped = apply(rs, in, rs.Sources)
		if rs.Private && rs.Token == "" {
			rs.Token = store.RandomHex(16)
		}
		if !rs.Private {
			rs.Token = ""
		}
		return nil
	})
	if errors.Is(err, store.ErrNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var gone []Source
	for _, s := range old {
		if slices.Contains(dropped, s.ID) {
			gone = append(gone, s)
		}
	}
	m.forgetSources(id, gone)
	m.removeStaleOutputs(rs)
	return rs, nil
}

// forgetSources deletes what dropped sources left behind: the downloaded
// copy of a link, and an uploaded file no rule set uses any more.
func (m *Manager) forgetSources(rsID string, gone []Source) {
	var keys []string
	for _, s := range gone {
		if s.State.CacheKey != "" {
			keys = append(keys, s.State.CacheKey)
		}
		os.Remove(m.sourcePath(rsID, s.ID))
	}
	if len(keys) > 0 {
		if _, err := m.storage.DeleteObjects(storage.DownloadsBucket, keys, nil); err != nil {
			m.log.Warn("delete downloaded copies", "err", err)
		}
	}
	m.dropUnusedUploads(gone)
}

// dropUnusedUploads removes uploaded source files that no rule set refers to.
func (m *Manager) dropUnusedUploads(candidates []Source) {
	var check []Source
	for _, s := range candidates {
		if s.Type == SourceObject && s.Bucket == storage.SourcesBucket {
			check = append(check, s)
		}
	}
	if len(check) == 0 {
		return
	}
	all, err := m.List()
	if err != nil {
		return
	}
	used := map[string]bool{}
	for _, rs := range all {
		for _, s := range rs.Sources {
			if s.Type == SourceObject {
				used[s.Bucket+"/"+s.Key] = true
			}
		}
	}
	for _, s := range check {
		if !used[s.Bucket+"/"+s.Key] {
			_, _ = m.storage.DeleteObjects(s.Bucket, []string{s.Key}, nil)
		}
	}
}

// Cleanup removes stored files nothing refers to any more: downloaded copies
// left from removed links, and uploads from forms that were never saved.
func (m *Manager) Cleanup() {
	all, err := m.List()
	if err != nil {
		return
	}
	used := map[string]bool{}
	for _, rs := range all {
		for _, s := range rs.Sources {
			if s.State.CacheKey != "" {
				used[storage.DownloadsBucket+"/"+s.State.CacheKey] = true
			}
			if s.Type == SourceObject {
				used[s.Bucket+"/"+s.Key] = true
			}
		}
	}
	for _, bucket := range []string{storage.DownloadsBucket, storage.SourcesBucket} {
		objs, err := m.storage.Walk(bucket)
		if err != nil {
			continue
		}
		// Give in-flight builds and unsaved forms time to reference new files.
		grace := time.Hour
		if bucket == storage.SourcesBucket {
			grace = 24 * time.Hour
		}
		var stale []string
		for _, o := range objs {
			if !used[bucket+"/"+o.Key] && time.Since(o.UpdatedAt) > grace {
				stale = append(stale, o.Key)
			}
		}
		if len(stale) > 0 {
			n, _ := m.storage.DeleteObjects(bucket, stale, nil)
			m.log.Info("removed unused files", "bucket", bucket, "count", n)
		}
	}
}

// apply copies user input onto a rule set and keeps fetch state of sources
// that did not change. It returns IDs of sources whose cache is now stale.
func apply(rs *Ruleset, in Input, old []Source) (dropped []string) {
	rs.Name, rs.Slug, rs.Description = in.Name, in.Slug, in.Description
	rs.Behavior, rs.Formats = in.Behavior, in.Formats
	rs.Include, rs.Exclude = in.Include, in.Exclude
	rs.Optimize, rs.AggregateIP, rs.NoResolve = in.Optimize, in.AggregateIP, in.NoResolve
	rs.Private, rs.Notify = in.Private, in.Notify
	if rs.IntervalMin != in.IntervalMin {
		rs.IntervalMin = in.IntervalMin
		rs.State.NextRunAt = nextRun(rs.State.LastRunAt, rs.IntervalMin)
	}
	rs.UpdatedAt = time.Now().UTC()

	prev := map[string]Source{}
	for _, s := range old {
		prev[s.ID] = s
	}
	kept := map[string]bool{}
	var sources []Source
	for _, si := range in.Sources {
		s := Source{ID: si.ID, Type: si.Type, URL: si.URL, Bucket: si.Bucket, Key: si.Key, Format: si.Format, BareAsSuffix: si.BareAsSuffix}
		if p, ok := prev[si.ID]; ok && !kept[si.ID] && p.Type == s.Type && p.URL == s.URL && p.Bucket == s.Bucket && p.Key == s.Key {
			s.State = p.State
		} else {
			s.ID = store.NewID()
			s.State = SourceState{Status: StatusPending}
		}
		kept[s.ID] = true
		sources = append(sources, s)
	}
	for _, s := range old {
		if !kept[s.ID] {
			dropped = append(dropped, s.ID)
		}
	}
	if sources == nil {
		sources = []Source{}
	}
	rs.Sources = sources
	return dropped
}

func nextRun(last time.Time, interval int) time.Time {
	if interval <= 0 || last.IsZero() {
		return time.Time{}
	}
	return last.Add(time.Duration(interval) * time.Minute)
}

func (m *Manager) Delete(id string) error {
	l := m.lock(id)
	l.Lock()
	defer l.Unlock()
	rs, err := m.Get(id)
	if err != nil {
		return err
	}
	if err := m.db.Delete(store.Rulesets, id); err != nil {
		return err
	}
	m.forgetSources(id, rs.Sources)
	_, _ = m.storage.DeleteObjects(storage.DownloadsBucket, nil, []string{rs.Slug + "/"})
	os.RemoveAll(filepath.Join(m.dir, "sources", id))
	os.RemoveAll(filepath.Join(m.dir, "outputs", id))
	os.Remove(m.statePath(id))
	m.cacheMu.Lock()
	delete(m.cache, id)
	m.cacheMu.Unlock()
	return nil
}

func (m *Manager) RotateToken(id string) (*Ruleset, error) {
	rs, err := store.Modify(m.db, store.Rulesets, id, func(rs *Ruleset) error {
		if !rs.Private {
			return invalid("токен нужен только приватному набору")
		}
		rs.Token = store.RandomHex(16)
		return nil
	})
	if errors.Is(err, store.ErrNotFound) {
		return nil, ErrNotFound
	}
	return rs, err
}

// EditEntries adds or removes individual rules. Adding cancels an earlier
// exclusion; removing cancels an earlier addition and records an exclusion
// with the chosen mode. The set is then rebuilt from cached sources.
func (m *Manager) EditEntries(ctx context.Context, id string, add []string, remove []rules.Exclusion) (*Ruleset, error) {
	addN, bad := rules.NormalizeEntries(add)
	if len(bad) > 0 {
		return nil, invalid("не удалось разобрать: %s", strings.Join(first(bad, 5), ", "))
	}
	remN, err := CleanExclusions(remove)
	if err != nil {
		return nil, err
	}
	l := m.lock(id)
	l.Lock()
	_, err = store.Modify(m.db, store.Rulesets, id, func(rs *Ruleset) error {
		for _, e := range addN {
			rs.Exclude = slices.DeleteFunc(rs.Exclude, func(x rules.Exclusion) bool { return x.Rule == e })
			if !slices.Contains(rs.Include, e) {
				rs.Include = append(rs.Include, e)
			}
		}
		for _, e := range remN {
			rs.Include = slices.DeleteFunc(rs.Include, func(x string) bool { return x == e.Rule })
			rs.Exclude = slices.DeleteFunc(rs.Exclude, func(x rules.Exclusion) bool { return x.Rule == e.Rule })
			rs.Exclude = append(rs.Exclude, e)
		}
		if len(rs.Include)+len(rs.Exclude) > 2*maxOverrides {
			return invalid("слишком много ручных правок")
		}
		rs.UpdatedAt = time.Now().UTC()
		return nil
	})
	l.Unlock()
	if errors.Is(err, store.ErrNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	rs, _, err := m.Refresh(ctx, id, false, "edit")
	return rs, err
}

// ClearOverrides drops chosen manual additions and exclusions.
func (m *Manager) ClearOverrides(ctx context.Context, id string, include, exclude []string) (*Ruleset, error) {
	l := m.lock(id)
	l.Lock()
	_, err := store.Modify(m.db, store.Rulesets, id, func(rs *Ruleset) error {
		rs.Include = slices.DeleteFunc(rs.Include, func(x string) bool { return slices.Contains(include, x) })
		rs.Exclude = slices.DeleteFunc(rs.Exclude, func(x rules.Exclusion) bool { return slices.Contains(exclude, x.Rule) })
		rs.UpdatedAt = time.Now().UTC()
		return nil
	})
	l.Unlock()
	if errors.Is(err, store.ErrNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	rs, _, err := m.Refresh(ctx, id, false, "edit")
	return rs, err
}

// PreviewExclusion shows what an exclusion would remove from the current set.
func (m *Manager) PreviewExclusion(id string, e rules.Exclusion) (rules.ExclusionPreview, error) {
	clean, err := CleanExclusions([]rules.Exclusion{e})
	if err != nil {
		return rules.ExclusionPreview{}, err
	}
	if len(clean) != 1 {
		return rules.ExclusionPreview{}, invalid("укажите одно правило")
	}
	_, lines, err := m.Lines(id)
	if err != nil {
		return rules.ExclusionPreview{}, err
	}
	pr, err := rules.Parse([]byte(strings.Join(lines, "\n")), rules.ParseOptions{Format: rules.InputText})
	if err != nil {
		return rules.ExclusionPreview{}, err
	}
	return pr.Set.PreviewExclusion(clean[0]), nil
}

func first(s []string, n int) []string { return firstN(s, n) }

func firstN[T any](s []T, n int) []T {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// ---- build -----------------------------------------------------------------

func (m *Manager) sourcePath(rsID, srcID string) string {
	return filepath.Join(m.dir, "sources", rsID, srcID)
}

func (m *Manager) OutputPath(rsID string, f rules.OutputFormat) string {
	return filepath.Join(m.dir, "outputs", rsID, string(f))
}

func (m *Manager) statePath(rsID string) string {
	return filepath.Join(m.dir, "state", rsID+".txt")
}

func (m *Manager) removeStaleOutputs(rs *Ruleset) {
	for _, f := range rules.AllOutputs {
		if !slices.Contains(rs.Formats, f) {
			os.Remove(m.OutputPath(rs.ID, f))
		}
	}
}

// Refresh rebuilds a rule set. With forceFetch every URL is downloaded again
// (conditionally); otherwise cached copies are reused when present.
func (m *Manager) Refresh(ctx context.Context, id string, forceFetch bool, trigger string) (*Ruleset, *Event, error) {
	select {
	case m.sem <- struct{}{}:
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	}
	defer func() { <-m.sem }()

	l := m.lock(id)
	l.Lock()
	defer l.Unlock()
	m.setRunning(id, true)
	defer m.setRunning(id, false)

	rs, err := m.Get(id)
	if err != nil {
		return nil, nil, err
	}
	started := time.Now()
	ev := &Event{ID: store.TimeKey(started), Time: started.UTC(), RulesetID: rs.ID, RulesetName: rs.Name, Trigger: trigger}

	combined := rules.NewSet()
	states := map[string]SourceState{}
	loaded := 0
	usedKeys := map[string]bool{}
	for _, src := range rs.Sources {
		if k := src.State.CacheKey; k != "" && strings.HasPrefix(k, rs.Slug+"/") {
			usedKeys[k] = true
		}
	}
	for _, src := range rs.Sources {
		data, st := m.loadSource(ctx, rs, src, forceFetch, usedKeys)
		if data != nil {
			pr, perr := rules.Parse(data, rules.ParseOptions{Format: src.Format, BareAsSuffix: src.BareAsSuffix})
			if perr != nil {
				st.Status, st.Error = StatusError, perr.Error()
			} else {
				loaded++
				st.Rules = pr.Set.Len()
				st.Invalid = pr.InvalidCount
				st.InvalidSample = first(pr.Invalid, 10)
				st.DetectedFormat = pr.Format
				st.DetectedBehavior = pr.Behavior
				combined.Merge(pr.Set)
			}
		}
		states[src.ID] = st
	}

	now := time.Now().UTC()
	fail := func(msg string) (*Ruleset, *Event, error) {
		ev.Kind, ev.Message = "error", msg
		saved, err := store.Modify(m.db, store.Rulesets, id, func(r *Ruleset) error {
			mergeSourceStates(r, states)
			r.State.Status, r.State.Error = StatusError, msg
			r.State.LastRunAt = now
			r.State.NextRunAt = nextRun(now, r.IntervalMin)
			r.State.DurationMs = time.Since(started).Milliseconds()
			return nil
		})
		m.addEvent(ev)
		if err != nil {
			return nil, ev, err
		}
		return saved, ev, nil
	}
	if len(rs.Sources) > 0 && loaded == 0 && len(rs.Include) == 0 {
		return fail("ни один источник не загрузился — оставлены прошлые файлы")
	}

	inc, _ := rules.ParseEntries(rs.Include)
	combined.Merge(inc)
	still := combined.ApplyExclusions(rs.Exclude)
	optimized := 0
	if rs.Optimize {
		optimized = combined.Optimize()
	}
	opts := rules.RenderOptions{AggregateIP: rs.AggregateIP, NoResolve: rs.NoResolve}
	built := rules.Resolve(combined, rs.Behavior, opts)
	if built.Count() == 0 {
		return fail("после обработки набор пуст — оставлены прошлые файлы")
	}

	outputs := map[rules.OutputFormat]OutputInfo{}
	if err := os.MkdirAll(filepath.Join(m.dir, "outputs", id), 0o700); err != nil {
		return nil, nil, err
	}
	written := 0
	for _, f := range rs.Formats {
		src := built
		if f == rules.OutMRS && built.Behavior == rules.BehaviorClassical {
			// MRS holds one kind: take the domains or the IP networks.
			mb, merr := rules.ResolveMRS(combined, rules.BehaviorAuto, opts)
			if merr != nil {
				outputs[f] = OutputInfo{Error: merr.Error()}
				continue
			}
			src = mb
		}
		data, rerr := src.Render(f)
		if rerr != nil {
			outputs[f] = OutputInfo{Error: rerr.Error()}
			continue
		}
		if werr := writeAtomic(m.OutputPath(id, f), data); werr != nil {
			outputs[f] = OutputInfo{Error: werr.Error()}
			continue
		}
		sum := sha256.Sum256(data)
		info := OutputInfo{Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:]), Verified: f == rules.OutMRS}
		if src != built {
			info.Behavior, info.Count, info.Dropped, info.Translated = src.Behavior, src.Count(), src.Dropped, src.Translated
		}
		outputs[f] = info
		written++
	}
	if written == 0 {
		msg := "ни один файл не собран"
		for _, f := range rs.Formats {
			if o := outputs[f]; o.Error != "" {
				msg = o.Error
				break
			}
		}
		return fail(msg)
	}

	lines := built.Lines()
	hash := hashLines(lines)
	prev := m.readState(id)
	added, removed := diff(prev, lines)
	if err := writeAtomic(m.statePath(id), joinLines(lines)); err != nil {
		return nil, nil, err
	}
	m.cacheMu.Lock()
	m.cache[id] = cachedLines{hash: hash, lines: lines}
	m.cacheMu.Unlock()

	ev.Total = len(lines)
	ev.Added, ev.Removed = len(added), len(removed)
	ev.SampleAdded, ev.SampleRemoved = first(added, sampleEntries), first(removed, sampleEntries)
	changed := hash != rs.State.Hash
	if changed {
		ev.Kind = "changed"
	} else {
		ev.Kind = "unchanged"
	}
	var warnings []string
	for _, st := range states {
		if st.Status == StatusError {
			warnings = append(warnings, "часть источников не загрузилась")
			break
		}
	}
	for _, f := range rs.Formats {
		if outputs[f].Error != "" {
			warnings = append(warnings, fmt.Sprintf("%s: %s", f, outputs[f].Error))
		}
	}
	ev.Message = strings.Join(warnings, "; ")

	saved, err := store.Modify(m.db, store.Rulesets, id, func(r *Ruleset) error {
		mergeSourceStates(r, states)
		r.State = State{
			Status:       StatusOK,
			LastRunAt:    now,
			LastChangeAt: r.State.LastChangeAt,
			NextRunAt:    nextRun(now, r.IntervalMin),
			Behavior:     built.Behavior,
			Count:        built.Count(),
			Domains:      len(built.Domains),
			CIDRs:        len(built.Prefixes),
			Classical:    len(built.Classical),
			Skipped:      built.Skipped,
			Dropped:      built.Dropped,
			Optimized:    optimized,
			Still:        firstN(still, 100),
			Outputs:      outputs,
			Hash:         hash,
			DurationMs:   time.Since(started).Milliseconds(),
		}
		if len(warnings) > 0 {
			r.State.Error = ev.Message
		}
		if changed {
			r.State.LastChangeAt = now
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	m.removeStaleOutputs(saved)
	if changed || trigger != "schedule" {
		m.addEvent(ev)
	}
	if changed && m.onChange != nil && saved.Notify && (trigger == "schedule" || trigger == "manual") && ev.Added+ev.Removed > 0 {
		m.onChange(saved, ev)
	}
	m.log.Info("rule set built", "id", id, "slug", saved.Slug, "rules", built.Count(),
		"added", len(added), "removed", len(removed), "trigger", trigger, "ms", saved.State.DurationMs)
	return saved, ev, nil
}

func mergeSourceStates(r *Ruleset, states map[string]SourceState) {
	for i := range r.Sources {
		if st, ok := states[r.Sources[i].ID]; ok {
			r.Sources[i].State = st
		}
	}
}

// loadSource returns the raw content of a source and its new state. Links
// are downloaded into the downloads bucket, one folder per rule set.
func (m *Manager) loadSource(ctx context.Context, rs *Ruleset, src Source, force bool, usedKeys map[string]bool) ([]byte, SourceState) {
	st := src.State
	st.Error = ""
	now := time.Now().UTC()
	switch src.Type {
	case SourceObject:
		st.CheckedAt = now
		obj, err := m.storage.Object(src.Bucket, src.Key)
		if err != nil {
			st.Status, st.Error = StatusError, fmt.Sprintf("%s/%s: %v", src.Bucket, src.Key, err)
			return nil, st
		}
		data, err := m.storage.ReadAll(obj, m.maxBytes)
		if err != nil {
			st.Status, st.Error = StatusError, err.Error()
			return nil, st
		}
		if st.SHA256 != obj.SHA256 {
			st.ChangedAt = now
		}
		st.Status, st.SHA256, st.Size = StatusOK, obj.SHA256, obj.Size
		return data, st

	case SourceURL:
		oldKey := st.CacheKey
		var cached []byte
		hasCache, legacy := false, false
		if oldKey != "" {
			if obj, err := m.storage.Object(storage.DownloadsBucket, oldKey); err == nil {
				if b, err := m.storage.ReadAll(obj, m.maxBytes); err == nil {
					cached, hasCache = b, true
				}
			}
		}
		if !hasCache {
			// copies kept on disk by 1.0.0–1.0.4
			if b, err := os.ReadFile(m.sourcePath(rs.ID, src.ID)); err == nil {
				cached, hasCache, legacy = b, true, true
			}
		}
		// The folder follows the rule set's current name.
		key := oldKey
		if key == "" || !strings.HasPrefix(key, rs.Slug+"/") {
			key = downloadKey(rs.Slug, src.URL, usedKeys)
			usedKeys[key] = true
		}
		keep := func(data []byte) {
			if _, err := m.storage.PutDownloaded(storage.DownloadsBucket, key, data, src.URL); err != nil {
				m.log.Warn("save downloaded source", "key", key, "err", err)
				return
			}
			st.CacheKey = key
			if oldKey != "" && oldKey != key {
				_, _ = m.storage.DeleteObjects(storage.DownloadsBucket, []string{oldKey}, nil)
			}
			if legacy {
				os.Remove(m.sourcePath(rs.ID, src.ID))
			}
		}
		moveCached := func() {
			if key != oldKey || legacy {
				keep(cached)
			}
		}
		if hasCache && !force {
			moveCached()
			return cached, st
		}
		etag, lm := "", ""
		if hasCache {
			etag, lm = st.ETag, st.LastModified
		}
		fctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		res, err := m.fetcher.Get(fctx, src.URL, etag, lm)
		cancel()
		st.CheckedAt = now
		if err != nil {
			st.Status, st.Error = StatusError, err.Error()
			m.log.Warn("source fetch failed", "ruleset", rs.ID, "url", fetch.RedactURL(src.URL), "err", err)
			if hasCache {
				moveCached()
				st.Error += " (используется прошлая копия)"
				return cached, st
			}
			return nil, st
		}
		if res.NotModified {
			moveCached()
			st.Status = StatusOK
			return cached, st
		}
		sum := sha256.Sum256(res.Body)
		hexSum := hex.EncodeToString(sum[:])
		if hexSum != st.SHA256 {
			st.ChangedAt = now
		}
		keep(res.Body)
		st.Status, st.SHA256, st.Size = StatusOK, hexSum, int64(len(res.Body))
		st.ETag, st.LastModified = res.ETag, res.LastModified
		return res.Body, st
	}
	st.Status, st.Error = StatusError, "неизвестный тип источника"
	return nil, st
}

// migrateLegacyCaches moves copies that 1.0.0–1.0.4 kept on disk into the
// downloads bucket, so they show up there right after an upgrade.
func (m *Manager) migrateLegacyCaches() {
	list, err := m.List()
	if err != nil {
		return
	}
	for _, rs := range list {
		l := m.lock(rs.ID)
		l.Lock()
		used := map[string]bool{}
		for _, s := range rs.Sources {
			if s.State.CacheKey != "" {
				used[s.State.CacheKey] = true
			}
		}
		moved := map[string]string{}
		for _, s := range rs.Sources {
			if s.Type != SourceURL || s.State.CacheKey != "" {
				continue
			}
			data, err := os.ReadFile(m.sourcePath(rs.ID, s.ID))
			if err != nil {
				continue
			}
			key := downloadKey(rs.Slug, s.URL, used)
			used[key] = true
			if _, err := m.storage.PutDownloaded(storage.DownloadsBucket, key, data, s.URL); err == nil {
				moved[s.ID] = key
			}
		}
		if len(moved) > 0 {
			_, err := store.Modify(m.db, store.Rulesets, rs.ID, func(r *Ruleset) error {
				for i := range r.Sources {
					if k, ok := moved[r.Sources[i].ID]; ok {
						r.Sources[i].State.CacheKey = k
					}
				}
				return nil
			})
			if err == nil {
				for id := range moved {
					os.Remove(m.sourcePath(rs.ID, id))
				}
				os.Remove(filepath.Join(m.dir, "sources", rs.ID))
				m.log.Info("moved downloaded copies into storage", "ruleset", rs.Slug, "files", len(moved))
			}
		}
		l.Unlock()
	}
}

// downloadKey names the copy of a link: <rule set>/<file name from the URL>,
// with a number added when two links share a name.
func downloadKey(slug, rawURL string, used map[string]bool) string {
	name := "source"
	if u, err := url.Parse(rawURL); err == nil {
		if b := path.Base(u.Path); b != "" && b != "/" && b != "." {
			name = b
		}
	}
	var clean strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			clean.WriteRune(r)
		default:
			clean.WriteByte('-')
		}
	}
	name = strings.Trim(clean.String(), "-.")
	if len(name) > 100 {
		name = name[len(name)-100:]
	}
	if name == "" {
		name = "source"
	}
	ext := path.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	key := slug + "/" + name
	for i := 2; used[key]; i++ {
		key = fmt.Sprintf("%s/%s-%d%s", slug, stem, i, ext)
	}
	return key
}

// ---- entries ---------------------------------------------------------------

func (m *Manager) lines(rs *Ruleset) []string {
	m.cacheMu.Lock()
	c, ok := m.cache[rs.ID]
	m.cacheMu.Unlock()
	if ok && c.hash == rs.State.Hash {
		return c.lines
	}
	lines := m.readState(rs.ID)
	m.cacheMu.Lock()
	if len(m.cache) > 16 {
		for k := range m.cache {
			delete(m.cache, k)
			break
		}
	}
	m.cache[rs.ID] = cachedLines{hash: rs.State.Hash, lines: lines}
	m.cacheMu.Unlock()
	return lines
}

// Lines returns every built rule of a set in its native text form.
func (m *Manager) Lines(id string) (*Ruleset, []string, error) {
	rs, err := m.Get(id)
	if err != nil {
		return nil, nil, err
	}
	return rs, m.lines(rs), nil
}

// EnableOptimizations switches on domain and/or IP optimization and rebuilds
// from cached sources. Used to clear redundancy found inside one set.
func (m *Manager) EnableOptimizations(ctx context.Context, id string, domains, ips bool) (*Ruleset, error) {
	l := m.lock(id)
	l.Lock()
	_, err := store.Modify(m.db, store.Rulesets, id, func(rs *Ruleset) error {
		rs.Optimize = rs.Optimize || domains
		rs.AggregateIP = rs.AggregateIP || ips
		rs.UpdatedAt = time.Now().UTC()
		return nil
	})
	l.Unlock()
	if errors.Is(err, store.ErrNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	rs, _, err := m.Refresh(ctx, id, false, "edit")
	return rs, err
}

// Entries pages through the built rules, optionally filtered by a substring.
func (m *Manager) Entries(id, query string, offset, limit int) (int, []string, error) {
	rs, err := m.Get(id)
	if err != nil {
		return 0, nil, err
	}
	all := m.lines(rs)
	q := strings.ToLower(strings.TrimSpace(query))
	var matched []string
	if q == "" {
		matched = all
	} else {
		for _, l := range all {
			if strings.Contains(strings.ToLower(l), q) {
				matched = append(matched, l)
			}
		}
	}
	total := len(matched)
	if offset > total {
		offset = total
	}
	end := min(offset+limit, total)
	page := slices.Clone(matched[offset:end])
	if page == nil {
		page = []string{}
	}
	return total, page, nil
}

func (m *Manager) readState(id string) []string {
	f, err := os.Open(m.statePath(id))
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		if l := sc.Text(); l != "" {
			out = append(out, l)
		}
	}
	return out
}

// ---- events ----------------------------------------------------------------

func (m *Manager) addEvent(ev *Event) {
	if err := m.db.Put(store.Events, ev.ID, ev); err != nil {
		m.log.Error("store event", "err", err)
		return
	}
	_ = m.db.TrimOldest(store.Events, eventsToKeep)
}

func (m *Manager) Events(rulesetID string, limit int) ([]Event, error) {
	var out []Event
	err := m.db.ScanReverse(store.Events, func(_ string, raw []byte) error {
		var ev Event
		if err := json.Unmarshal(raw, &ev); err != nil {
			return err
		}
		if rulesetID == "" || ev.RulesetID == rulesetID {
			out = append(out, ev)
		}
		if len(out) >= limit {
			return store.ErrStop
		}
		return nil
	})
	if out == nil {
		out = []Event{}
	}
	return out, err
}

// ---- access ----------------------------------------------------------------

// AddDownloads adds fetch counts to rule sets, keyed by ID and format. Sets
// deleted in the meantime are skipped.
func (m *Manager) AddDownloads(delta map[string]map[rules.OutputFormat]store.Counter) error {
	ids := make([]string, 0, len(delta))
	for id := range delta {
		ids = append(ids, id)
	}
	return store.ModifyEach(m.db, store.Rulesets, ids, func(id string, rs *Ruleset) {
		if rs.Downloads == nil {
			rs.Downloads = map[rules.OutputFormat]store.Counter{}
		}
		for f, c := range delta[id] {
			cur := rs.Downloads[f]
			cur.Add(c)
			rs.Downloads[f] = cur
		}
	})
}

// CheckToken validates the access token of a private rule set.
func CheckToken(rs *Ruleset, token string) bool {
	if !rs.Private {
		return true
	}
	return rs.Token != "" && subtle.ConstantTimeCompare([]byte(rs.Token), []byte(token)) == 1
}

// ---- scheduler -------------------------------------------------------------

func (m *Manager) setRunning(id string, v bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if v {
		m.running[id] = true
	} else {
		delete(m.running, id)
	}
}

func (m *Manager) IsRunning(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.running[id]
}

// Run rebuilds rule sets when their interval elapses, and builds new ones.
func (m *Manager) Run(ctx context.Context) {
	tick := time.NewTicker(20 * time.Second)
	defer tick.Stop()
	clean := time.NewTicker(time.Hour)
	defer clean.Stop()
	m.migrateLegacyCaches()
	m.schedule(ctx)
	m.Cleanup()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			m.schedule(ctx)
		case <-clean.C:
			m.Cleanup()
		}
	}
}

func (m *Manager) schedule(ctx context.Context) {
	list, err := m.List()
	if err != nil {
		m.log.Error("scheduler list", "err", err)
		return
	}
	now := time.Now()
	for _, rs := range list {
		due := rs.State.LastRunAt.IsZero() ||
			(rs.IntervalMin > 0 && !rs.State.NextRunAt.IsZero() && !now.Before(rs.State.NextRunAt))
		if !due || m.IsRunning(rs.ID) {
			continue
		}
		m.setRunning(rs.ID, true)
		go func(id string, first bool) {
			defer m.setRunning(id, false)
			trigger := "schedule"
			if first {
				trigger = "create"
			}
			if _, _, err := m.Refresh(ctx, id, true, trigger); err != nil && !errors.Is(err, context.Canceled) {
				m.log.Warn("scheduled build failed", "id", id, "err", err)
			}
		}(rs.ID, rs.State.LastRunAt.IsZero())
	}
}

// ---- helpers ---------------------------------------------------------------

func writeAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func joinLines(lines []string) []byte {
	var b bytes.Buffer
	for _, l := range lines {
		b.WriteString(l)
		b.WriteByte('\n')
	}
	return b.Bytes()
}

func hashLines(lines []string) string {
	h := sha256.New()
	for _, l := range lines {
		h.Write([]byte(l))
		h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func diff(old, cur []string) (added, removed []string) {
	o := make(map[string]struct{}, len(old))
	for _, l := range old {
		o[l] = struct{}{}
	}
	c := make(map[string]struct{}, len(cur))
	for _, l := range cur {
		c[l] = struct{}{}
		if _, ok := o[l]; !ok {
			added = append(added, l)
		}
	}
	for _, l := range old {
		if _, ok := c[l]; !ok {
			removed = append(removed, l)
		}
	}
	return added, removed
}
