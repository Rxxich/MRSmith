package rulesets

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"unicode/utf8"

	"mrsmith/internal/fetch"
	"mrsmith/internal/rules"
	"mrsmith/internal/storage"
	"mrsmith/internal/store"
)

// Pause waits for running builds and cleanups to finish and holds new ones
// until resume is called, so a restore can swap every set at once.
func (m *Manager) Pause() (resume func()) {
	m.gate.Lock()
	return m.gate.Unlock
}

// Restored forgets what the panel kept for sets a restore replaced: built
// files, the lines of the last build and in-memory caches. Call it while
// paused, with the IDs of the old and the new sets.
func (m *Manager) Restored(ids []string) {
	for _, id := range ids {
		if !store.IsID(id) {
			continue
		}
		os.RemoveAll(filepath.Join(m.dir, "outputs", id))
		os.RemoveAll(filepath.Join(m.dir, "sources", id))
		os.Remove(m.statePath(id))
	}
	m.cacheMu.Lock()
	m.cache = map[string]cachedLines{}
	m.cacheMu.Unlock()
	m.forgetSlugs()
	if err := m.storage.EnsureManagedBucket(storage.DownloadsBucket); err != nil {
		m.log.Warn("restore: downloads bucket", "err", err)
	}
	if err := m.storage.EnsureSystemBucket(storage.SourcesBucket); err != nil {
		m.log.Warn("restore: sources bucket", "err", err)
	}
}

// RebuildAll builds every set again, one at a time, from the copies at hand:
// downloaded sources are reused, nothing is fetched unless a copy is missing.
func (m *Manager) RebuildAll(ctx context.Context, trigger string) {
	list, err := m.List()
	if err != nil {
		m.log.Error("rebuild after restore", "err", err)
		return
	}
	for _, rs := range list {
		if ctx.Err() != nil {
			return
		}
		if _, _, err := m.Refresh(ctx, rs.ID, false, trigger); err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, ErrInterrupted) {
			m.log.Warn("rebuild after restore", "id", rs.ID, "err", err)
		}
	}
}

// PrepareRestore checks rule sets read from a backup and readies them to be
// stored: rebase moves links to the old panel address onto the new one, and
// every set is marked to be built again. It returns how many links moved.
func PrepareRestore(list []Ruleset, rebase func(string) (string, bool)) ([]Ruleset, int, error) {
	ids, slugs := map[string]bool{}, map[string]bool{}
	moved := 0
	out := make([]Ruleset, 0, len(list))
	for i, rs := range list {
		name := rs.Name
		if name == "" {
			name = fmt.Sprintf("№%d", i+1)
		}
		bad := func(format string, a ...any) error {
			return fmt.Errorf("набор «%s»: %s", name, fmt.Sprintf(format, a...))
		}
		if !store.IsID(rs.ID) || ids[rs.ID] {
			return nil, 0, bad("неверный или повторяющийся ID")
		}
		ids[rs.ID] = true
		if n := utf8.RuneCountInString(rs.Name); n == 0 || n > 80 {
			return nil, 0, bad("название пустое или длиннее 80 символов")
		}
		if !slugRe.MatchString(rs.Slug) || slugs[rs.Slug] {
			return nil, 0, bad("неверное или повторяющееся имя файла %q", rs.Slug)
		}
		slugs[rs.Slug] = true
		if rs.Behavior == "" {
			rs.Behavior = rules.BehaviorAuto
		}
		if !rs.Behavior.Valid() {
			return nil, 0, bad("неизвестный behavior %q", rs.Behavior)
		}
		if len(rs.Formats) == 0 {
			return nil, 0, bad("нет выходных форматов")
		}
		for _, f := range rs.Formats {
			if !f.Valid() {
				return nil, 0, bad("неизвестный формат %q", f)
			}
		}
		if len(rs.Sources) > maxSources || len(rs.Include) > maxOverrides || len(rs.Exclude) > maxOverrides {
			return nil, 0, bad("слишком много источников или ручных правок")
		}
		if rs.IntervalMin != 0 && (rs.IntervalMin < MinIntervalM || rs.IntervalMin > MaxIntervalM) {
			rs.IntervalMin = 0
		}
		for j := range rs.Sources {
			s := &rs.Sources[j]
			if !store.IsID(s.ID) {
				s.ID = store.NewID()
			}
			if s.Format == "" {
				s.Format = rules.InputAuto
			}
			if !s.Format.Valid() {
				return nil, 0, bad("источник %d: неизвестный формат %q", j+1, s.Format)
			}
			switch s.Type {
			case SourceURL:
				if u, ok := rebase(s.URL); ok {
					s.URL = u
					moved++
				}
				if err := fetch.ValidateURL(s.URL); err != nil {
					return nil, 0, bad("источник %d: %v", j+1, err)
				}
			case SourceObject:
				if storage.ValidBucketName(s.Bucket) != nil {
					return nil, 0, bad("источник %d: неверный бакет %q", j+1, s.Bucket)
				}
				if _, err := storage.CleanKey(s.Key); err != nil {
					return nil, 0, bad("источник %d: %v", j+1, err)
				}
			default:
				return nil, 0, bad("источник %d: неизвестный тип %q", j+1, s.Type)
			}
		}
		if rs.Include == nil {
			rs.Include = []string{}
		}
		if rs.Exclude == nil {
			rs.Exclude = []rules.Exclusion{}
		}
		if rs.Sources == nil {
			rs.Sources = []Source{}
		}
		switch {
		case rs.Private && len(rs.Token) < 16:
			rs.Token = store.RandomHex(16)
		case !rs.Private:
			rs.Token = ""
		}
		// Built files are not in a backup: build every set again.
		rs.State.InputHash = ""
		rs.State.Status, rs.State.Error = StatusPending, ""
		rs.State.Outputs = nil
		out = append(out, rs)
	}
	return out, moved, nil
}
