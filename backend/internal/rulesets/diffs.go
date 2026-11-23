package rulesets

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"mrsmith/internal/apperr"
	"mrsmith/internal/store"
)

// An event keeps a sample of what a build added and removed. The full lists
// go to diffs/<set>/<event>.gz, "+rule" and "-rule" lines, for the latest
// builds of each set.
const diffsToKeep = 30

var (
	eventIDRe       = regexp.MustCompile(`^[0-9a-f]{24}$`)
	ErrDiffNotFound = apperr.NotFound("полный список этого обновления не сохранился: панель хранит его для последних 30 обновлений каждого набора")
)

func (m *Manager) diffDir(rsID string) string { return filepath.Join(m.dir, "diffs", rsID) }

func (m *Manager) diffPath(rsID, evID string) string {
	return filepath.Join(m.diffDir(rsID), evID+".gz")
}

// saveDiff stores every rule a build added and removed, then drops the
// oldest lists beyond diffsToKeep.
func (m *Manager) saveDiff(rsID, evID string, added, removed []string) error {
	if err := os.MkdirAll(m.diffDir(rsID), 0o700); err != nil {
		return err
	}
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	w := bufio.NewWriter(zw)
	for _, part := range []struct {
		sign  byte
		lines []string
	}{{'+', added}, {'-', removed}} {
		for _, l := range part.lines {
			w.WriteByte(part.sign)
			w.WriteString(l)
			w.WriteByte('\n')
		}
	}
	if err := w.Flush(); err != nil {
		return err
	}
	if err := zw.Close(); err != nil {
		return err
	}
	if err := writeAtomic(m.diffPath(rsID, evID), buf.Bytes()); err != nil {
		return err
	}
	m.pruneDiffs(rsID)
	return nil
}

func (m *Manager) pruneDiffs(rsID string) {
	entries, err := os.ReadDir(m.diffDir(rsID))
	if err != nil {
		return
	}
	var names []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".gz") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names) // event IDs sort by time
	for len(names) > diffsToKeep {
		os.Remove(filepath.Join(m.diffDir(rsID), names[0]))
		names = names[1:]
	}
}

// dropOrphanDiffs removes lists of sets that no longer exist.
func (m *Manager) dropOrphanDiffs(live map[string]bool) {
	entries, err := os.ReadDir(filepath.Join(m.dir, "diffs"))
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() && !live[e.Name()] {
			os.RemoveAll(filepath.Join(m.dir, "diffs", e.Name()))
		}
	}
}

// Diff returns one page of what a build added ("added") or removed
// ("removed"), optionally only rules containing query, and how many there
// are of each kind in total.
func (m *Manager) Diff(rsID, evID, kind, query string, offset, limit int) (*DiffPage, error) {
	if !store.IsID(rsID) || !eventIDRe.MatchString(evID) {
		return nil, ErrDiffNotFound
	}
	var sign byte
	switch kind {
	case "added", "":
		sign, kind = '+', "added"
	case "removed":
		sign = '-'
	default:
		return nil, invalid("kind: added или removed")
	}
	f, err := os.Open(m.diffPath(rsID, evID))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrDiffNotFound
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	query = strings.ToLower(strings.TrimSpace(query))
	page := &DiffPage{Kind: kind, Items: []string{}}
	sc := bufio.NewScanner(zr)
	sc.Buffer(make([]byte, 0, 4096), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}
		switch line[0] {
		case '+':
			page.Added++
		case '-':
			page.Removed++
		}
		if line[0] != sign || query != "" && !strings.Contains(strings.ToLower(line[1:]), query) {
			continue
		}
		if page.Total >= offset && page.Total < offset+limit {
			page.Items = append(page.Items, line[1:])
		}
		page.Total++
	}
	return page, sc.Err()
}

// DiffPage is one page of a build's full change list.
type DiffPage struct {
	Kind    string   `json:"kind"`
	Added   int      `json:"added"`
	Removed int      `json:"removed"`
	Total   int      `json:"total"` // matching rules of this kind
	Items   []string `json:"items"`
}
