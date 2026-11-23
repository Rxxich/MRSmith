package backup

import (
	"archive/zip"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"mrsmith/internal/rulesets"
	"mrsmith/internal/storage"
	"mrsmith/internal/store"
)

// An archive is a zip with three JSON files and the stored content:
//
//	manifest.json  what is inside, when and where it was made
//	rulesets.json  rule sets with their sources, manual edits and settings
//	storage.json   buckets and file records
//	files/<sha256> file content, one copy per distinct content
//
// Built files are left out: the panel builds them again after a restore.
const (
	formatVersion = 1
	appName       = "mrsmith"
	manifestName  = "manifest.json"
	rulesetsName  = "rulesets.json"
	storageName   = "storage.json"
	filesDir      = "files/"

	maxJSON      = 256 << 20
	maxManifest  = 1 << 20
	maxFileSize  = 4 << 30
	maxTotalSize = 20 << 30
	namesInList  = 20
)

const (
	KindAuto          = "auto"
	KindManual        = "manual"
	KindBeforeRestore = "before-restore"
)

// Manifest describes an archive. It is read for the list without opening
// anything else.
type Manifest struct {
	App       string    `json:"app"`
	Format    int       `json:"format"`
	Version   string    `json:"version"`
	CreatedAt time.Time `json:"createdAt"`
	Kind      string    `json:"kind"`
	// Server tells archives made by this panel from ones brought over.
	Server string `json:"server,omitempty"`
	// LinkBase is the address public links started with, e.g.
	// https://rules.example.com. A restore elsewhere moves links from it.
	LinkBase string   `json:"linkBase,omitempty"`
	Rulesets int      `json:"rulesets"`
	Names    []string `json:"names,omitempty"`
	Buckets  int      `json:"buckets"`
	Objects  int      `json:"objects"`
	Bytes    int64    `json:"bytes"`
	// Skipped counts files deleted while the archive was being written.
	Skipped int `json:"skipped,omitempty"`
}

type storageDump struct {
	Buckets []storage.Bucket `json:"buckets"`
	Objects []storage.Object `json:"objects"`
}

// RestoreResult says what a restore did, for the panel to explain.
type RestoreResult struct {
	CreatedAt time.Time `json:"createdAt"`
	Rulesets  int       `json:"rulesets"`
	Objects   int       `json:"objects"`
	Bytes     int64     `json:"bytes"`
	OldBase   string    `json:"oldBase,omitempty"`
	NewBase   string    `json:"newBase,omitempty"`
	// Rebased counts sources that pointed at the old panel address.
	Rebased int `json:"rebased"`
	// Safety is the archive with the data as it was before the restore.
	Safety string `json:"safety,omitempty"`
}

// ---- create ----------------------------------------------------------------

// create writes a new archive of the current rule sets and storage and
// returns its file name.
func (s *Service) create(kind, linkBase string) (string, *Manifest, error) {
	if err := s.folder(); err != nil {
		return "", nil, err
	}
	m := s.meta()
	now := s.now().UTC()
	man := &Manifest{
		App: appName, Format: formatVersion, Version: s.opt.Version, CreatedAt: now, Kind: kind,
		Server: m.Server, LinkBase: s.linkBase(linkBase, m),
	}

	var sets []rulesets.Ruleset
	var buckets []storage.Bucket
	var objects []storage.Object
	err := s.db.ReadTables([][]byte{store.Rulesets, store.Buckets, store.Objects}, func(t []byte, _ string, raw []byte) error {
		switch string(t) {
		case string(store.Rulesets):
			var rs rulesets.Ruleset
			if err := json.Unmarshal(raw, &rs); err != nil {
				return err
			}
			sets = append(sets, rs)
		case string(store.Buckets):
			var b storage.Bucket
			if err := json.Unmarshal(raw, &b); err != nil {
				return err
			}
			buckets = append(buckets, b)
		case string(store.Objects):
			o, err := storage.DecodeObject(raw)
			if err != nil {
				return err
			}
			objects = append(objects, o)
		}
		return nil
	})
	if err != nil {
		return "", nil, fmt.Errorf("не удалось прочитать данные панели: %w", err)
	}
	sort.Slice(sets, func(i, j int) bool { return sets[i].CreatedAt.Before(sets[j].CreatedAt) })

	tmp, err := os.CreateTemp(s.opt.Dir, ".mrsmith-*.tmp")
	if err != nil {
		return "", nil, fmt.Errorf("не удалось создать файл в папке бэкапов: %w", err)
	}
	ok := false
	defer func() {
		if !ok {
			tmp.Close()
			os.Remove(tmp.Name())
		}
	}()
	zw := zip.NewWriter(tmp)
	inArchive := map[string]bool{}
	kept := make([]storage.Object, 0, len(objects))
	for _, o := range objects {
		if !inArchive[o.SHA256] {
			if err := s.addContent(zw, o); err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					man.Skipped++ // deleted while we were writing
					continue
				}
				return "", nil, fmt.Errorf("файл %s/%s: %w", o.Bucket, o.Key, err)
			}
			inArchive[o.SHA256] = true
		}
		kept = append(kept, o)
		man.Bytes += o.Size
	}
	if sets == nil {
		sets = []rulesets.Ruleset{}
	}
	if buckets == nil {
		buckets = []storage.Bucket{}
	}
	man.Rulesets, man.Buckets, man.Objects = len(sets), len(buckets), len(kept)
	for _, rs := range sets {
		if len(man.Names) == namesInList {
			break
		}
		man.Names = append(man.Names, rs.Name)
	}
	for _, part := range []struct {
		name string
		v    any
	}{{storageName, storageDump{Buckets: buckets, Objects: kept}}, {rulesetsName, sets}, {manifestName, man}} {
		if err := writeJSON(zw, part.name, part.v, now); err != nil {
			return "", nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return "", nil, err
	}
	if err := tmp.Sync(); err != nil {
		return "", nil, err
	}
	if err := tmp.Close(); err != nil {
		return "", nil, err
	}
	name := s.freeName(now, kind)
	if err := os.Rename(tmp.Name(), filepath.Join(s.opt.Dir, name)); err != nil {
		return "", nil, err
	}
	ok = true
	s.log.Info("backup created", "file", name, "rulesets", man.Rulesets, "objects", man.Objects, "kind", kind)
	return name, man, nil
}

func (s *Service) addContent(zw *zip.Writer, o storage.Object) error {
	f, err := s.st.OpenBlob(o.BlobID)
	if err != nil {
		return err
	}
	defer f.Close()
	w, err := zw.CreateHeader(&zip.FileHeader{Name: filesDir + o.SHA256, Method: zip.Deflate, Modified: o.UpdatedAt})
	if err != nil {
		return err
	}
	_, err = io.Copy(w, f)
	return err
}

func writeJSON(zw *zip.Writer, name string, v any, at time.Time) error {
	w, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate, Modified: at})
	if err != nil {
		return err
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", " ")
	return enc.Encode(v)
}

// freeName picks a file name for a new archive that is not taken yet.
func (s *Service) freeName(t time.Time, kind string) string {
	base := "mrsmith-" + t.Format("2006-01-02-1504")
	switch kind {
	case KindAuto:
		base += "-auto"
	case KindBeforeRestore:
		base += "-before-restore"
	}
	name := base + ".zip"
	for i := 2; ; i++ {
		if _, err := os.Lstat(filepath.Join(s.opt.Dir, name)); errors.Is(err, fs.ErrNotExist) {
			return name
		}
		name = fmt.Sprintf("%s-%d.zip", base, i)
	}
}

// ---- read ------------------------------------------------------------------

var errNotBackup = errors.New("это не бэкап MRSmith: внутри нет manifest.json")

// readManifest opens an archive just far enough to describe it.
func readManifest(path string) (*Manifest, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return nil, notZip(err)
	}
	defer zr.Close()
	return manifestOf(index(&zr.Reader))
}

func notZip(err error) error {
	if errors.Is(err, zip.ErrFormat) || errors.Is(err, io.ErrUnexpectedEOF) {
		return errors.New("файл не похож на zip-архив или скопирован не до конца")
	}
	if errors.Is(err, fs.ErrPermission) {
		return errors.New("панели не хватает прав прочитать файл — в папке с docker-compose.yml выполните: chown -R 65532:65532 backup")
	}
	return err
}

func index(zr *zip.Reader) map[string]*zip.File {
	files := make(map[string]*zip.File, len(zr.File))
	for _, f := range zr.File {
		files[f.Name] = f
	}
	return files
}

func manifestOf(files map[string]*zip.File) (*Manifest, error) {
	f := files[manifestName]
	if f == nil {
		return nil, errNotBackup
	}
	var m Manifest
	if err := readJSON(f, &m, maxManifest); err != nil {
		return nil, fmt.Errorf("manifest.json: %w", err)
	}
	if m.App != appName {
		return nil, errNotBackup
	}
	if m.Format < 1 {
		return nil, errors.New("неизвестный формат бэкапа")
	}
	return &m, nil
}

func readJSON(f *zip.File, v any, limit int64) error {
	if f.UncompressedSize64 > uint64(limit) {
		return errors.New("слишком большой файл внутри архива")
	}
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	data, err := io.ReadAll(io.LimitReader(rc, limit+1))
	if err != nil {
		if errors.Is(err, zip.ErrChecksum) {
			return errors.New("архив повреждён")
		}
		return err
	}
	if int64(len(data)) > limit {
		return errors.New("слишком большой файл внутри архива")
	}
	if err := json.Unmarshal(data, v); err != nil {
		return errors.New("архив повреждён: не читается " + f.Name)
	}
	return nil
}

// ---- restore ---------------------------------------------------------------

// restore replaces every rule set and the whole storage with the contents
// of an archive. Nothing changes until the archive has been checked and its
// files written; the current data is saved into a separate archive first.
func (s *Service) restore(ctx context.Context, name, linkBase string) (*RestoreResult, error) {
	path, err := s.path(name)
	if err != nil {
		return nil, err
	}
	zr, err := zip.OpenReader(path)
	if err != nil {
		return nil, notZip(err)
	}
	defer zr.Close()
	files := index(&zr.Reader)
	man, err := manifestOf(files)
	if err != nil {
		return nil, err
	}
	if man.Format > formatVersion {
		return nil, fmt.Errorf("бэкап сделан более новой версией панели (%s) — обновите панель и попробуйте снова", man.Version)
	}

	var sets []rulesets.Ruleset
	var dump storageDump
	for _, part := range []struct {
		name string
		v    any
	}{{rulesetsName, &sets}, {storageName, &dump}} {
		f := files[part.name]
		if f == nil {
			return nil, fmt.Errorf("в архиве нет %s — он неполный", part.name)
		}
		if err := readJSON(f, part.v, maxJSON); err != nil {
			return nil, fmt.Errorf("%s: %w", part.name, err)
		}
	}
	buckets, err := checkBuckets(dump.Buckets)
	if err != nil {
		return nil, err
	}
	if err := checkObjects(dump.Objects, buckets, files); err != nil {
		return nil, err
	}

	m := s.meta()
	res := &RestoreResult{CreatedAt: man.CreatedAt, OldBase: man.LinkBase, NewBase: s.linkBase(linkBase, m)}
	rebase := rebaser(res.OldBase, res.NewBase)
	sets, res.Rebased, err = rulesets.PrepareRestore(sets, rebase)
	if err != nil {
		return nil, err
	}
	for i := range dump.Objects {
		if u, ok := rebase(dump.Objects[i].Origin); ok {
			dump.Objects[i].Origin = u
		}
	}

	if s.db.Count(store.Rulesets) > 0 || s.db.Count(store.Objects) > 0 {
		safety, _, err := s.create(KindBeforeRestore, linkBase)
		if err != nil {
			return nil, fmt.Errorf("не удалось сохранить текущие данные перед восстановлением, поэтому ничего не менялось: %w", err)
		}
		res.Safety = safety
	}

	// Write the contents under new IDs. Until the records are swapped
	// nothing points at them, and a failure just removes them again.
	var fresh []string
	drop := func() {
		for _, id := range fresh {
			s.st.RemoveBlob(id)
		}
	}
	for i := range dump.Objects {
		if ctx.Err() != nil {
			drop()
			return nil, ctx.Err()
		}
		o := &dump.Objects[i]
		id, err := s.writeContent(files[filesDir+o.SHA256], o)
		if err != nil {
			drop()
			return nil, err
		}
		fresh = append(fresh, id)
		o.BlobID = id
		res.Bytes += o.Size
	}

	recs := map[string][]store.Record{string(store.Rulesets): nil, string(store.Buckets): nil, string(store.Objects): nil}
	newIDs := make([]string, 0, len(sets))
	for _, rs := range sets {
		raw, err := json.Marshal(rs)
		if err != nil {
			drop()
			return nil, err
		}
		recs[string(store.Rulesets)] = append(recs[string(store.Rulesets)], store.Record{Key: rs.ID, Raw: raw})
		newIDs = append(newIDs, rs.ID)
	}
	for _, b := range buckets {
		raw, _ := json.Marshal(b)
		recs[string(store.Buckets)] = append(recs[string(store.Buckets)], store.Record{Key: b.Name, Raw: raw})
	}
	for _, o := range dump.Objects {
		raw, err := storage.EncodeObject(o)
		if err != nil {
			drop()
			return nil, err
		}
		recs[string(store.Objects)] = append(recs[string(store.Objects)], store.Record{Key: storage.ObjectKey(o.Bucket, o.Key), Raw: raw})
	}

	resume := s.rs.Pause()
	var oldSets, oldBlobs []string
	err = s.db.ReplaceTables(recs, func(table, key string, raw []byte) {
		switch table {
		case string(store.Rulesets):
			oldSets = append(oldSets, key)
		case string(store.Objects):
			if o, err := storage.DecodeObject(raw); err == nil {
				oldBlobs = append(oldBlobs, o.BlobID)
			}
		}
	})
	if err != nil {
		resume()
		drop()
		return nil, fmt.Errorf("не удалось записать данные: %w", err)
	}
	s.rs.Restored(append(oldSets, newIDs...))
	resume()
	for _, id := range oldBlobs {
		s.st.RemoveBlob(id)
	}
	res.Rulesets, res.Objects = len(sets), len(dump.Objects)
	s.log.Info("backup restored", "file", name, "rulesets", res.Rulesets, "objects", res.Objects, "rebased", res.Rebased)
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.rs.RebuildAll(s.bg, "restore")
	}()
	return res, nil
}

func (s *Service) writeContent(f *zip.File, o *storage.Object) (string, error) {
	rc, err := f.Open()
	if err != nil {
		return "", err
	}
	defer rc.Close()
	id, size, sum, err := s.st.WriteBlob(rc, o.Size)
	if err != nil {
		if errors.Is(err, zip.ErrChecksum) || errors.Is(err, storage.ErrTooLarge) {
			return "", fmt.Errorf("архив повреждён: файл %s/%s не совпадает с описанием", o.Bucket, o.Key)
		}
		return "", fmt.Errorf("файл %s/%s: %w", o.Bucket, o.Key, err)
	}
	if size != o.Size || sum != o.SHA256 {
		s.st.RemoveBlob(id)
		return "", fmt.Errorf("архив повреждён: файл %s/%s не совпадает с описанием", o.Bucket, o.Key)
	}
	return id, nil
}

// checkBuckets validates buckets from an archive and makes sure the ones the
// panel relies on are there with their usual flags.
func checkBuckets(list []storage.Bucket) ([]storage.Bucket, error) {
	seen := map[string]bool{}
	out := make([]storage.Bucket, 0, len(list)+2)
	for _, b := range list {
		if err := storage.ValidBucketName(b.Name); err != nil || seen[b.Name] {
			return nil, fmt.Errorf("архив повреждён: неверный или повторяющийся бакет %q", b.Name)
		}
		seen[b.Name] = true
		b.System = b.Name == storage.SourcesBucket || b.Name == storage.DownloadsBucket
		b.Managed = b.Name == storage.DownloadsBucket
		if b.System {
			b.Public = false
		}
		out = append(out, b)
	}
	for _, name := range []string{storage.SourcesBucket, storage.DownloadsBucket} {
		if !seen[name] {
			out = append(out, storage.Bucket{Name: name, System: true, Managed: name == storage.DownloadsBucket, CreatedAt: time.Now().UTC()})
		}
	}
	return out, nil
}

func checkObjects(list []storage.Object, buckets []storage.Bucket, files map[string]*zip.File) error {
	known := map[string]bool{}
	for _, b := range buckets {
		known[b.Name] = true
	}
	seen := map[string]bool{}
	var total int64
	for _, o := range list {
		where := o.Bucket + "/" + o.Key
		if !known[o.Bucket] {
			return fmt.Errorf("архив повреждён: файл %s в несуществующем бакете", where)
		}
		if k, err := storage.CleanKey(o.Key); err != nil || k != o.Key || seen[where] {
			return fmt.Errorf("архив повреждён: неверное или повторяющееся имя файла %q", where)
		}
		seen[where] = true
		if len(o.SHA256) != 64 || !isHex(o.SHA256) || o.Size < 0 || o.Size > maxFileSize {
			return fmt.Errorf("архив повреждён: неверное описание файла %s", where)
		}
		f := files[filesDir+o.SHA256]
		if f == nil {
			return fmt.Errorf("в архиве нет содержимого файла %s — он неполный", where)
		}
		if f.UncompressedSize64 != uint64(o.Size) {
			return fmt.Errorf("архив повреждён: размер файла %s не совпадает", where)
		}
		total += o.Size
		if total > maxTotalSize {
			return fmt.Errorf("в архиве больше %d ГБ файлов — так много панель не восстанавливает", maxTotalSize>>30)
		}
	}
	return nil
}

func isHex(s string) bool {
	_, err := hex.DecodeString(s)
	return err == nil && strings.ToLower(s) == s
}

// rebaser moves public links of the old panel address (/r/… and /f/…) onto
// the new one. Other links stay as they are.
func rebaser(oldBase, newBase string) func(string) (string, bool) {
	if oldBase == "" || newBase == "" || strings.EqualFold(oldBase, newBase) {
		return func(string) (string, bool) { return "", false }
	}
	return func(u string) (string, bool) {
		for _, p := range []string{"/r/", "/f/"} {
			prefix := oldBase + p
			if len(u) > len(prefix) && strings.EqualFold(u[:len(prefix)], prefix) {
				return newBase + u[len(oldBase):], true
			}
		}
		return "", false
	}
}
