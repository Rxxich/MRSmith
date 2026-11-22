// Package storage is a small S3-like object store: buckets hold objects
// addressed by slash-separated keys. Content lives on disk under random IDs;
// user-supplied names never reach the filesystem.
package storage

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"mrsmith/internal/apperr"
	"mrsmith/internal/store"
)

type Bucket struct {
	Name   string `json:"name"`
	Public bool   `json:"public"`
	System bool   `json:"system"`
	// Managed buckets are filled by the panel itself and read-only for users.
	Managed   bool      `json:"managed"`
	CreatedAt time.Time `json:"createdAt"`
}

type Object struct {
	Bucket      string    `json:"bucket"`
	Key         string    `json:"key"`
	Size        int64     `json:"size"`
	ContentType string    `json:"contentType"`
	SHA256      string    `json:"sha256"`
	BlobID      string    `json:"-"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
	// Origin is the URL the panel downloaded the file from, if any.
	Origin string `json:"origin,omitempty"`
	// Downloads counts fetches through the public /f/ link.
	Downloads store.Counter `json:"downloads,omitzero"`
}

// objectRecord is the stored form; BlobID must persist but never leave the server.
type objectRecord struct {
	Object
	Blob string `json:"blob"`
}

func (r objectRecord) toObject() Object {
	o := r.Object
	o.BlobID = r.Blob
	return o
}

const (
	// SourcesBucket keeps files uploaded from a computer as rule sources.
	SourcesBucket = "rule-sources"
	// DownloadsBucket keeps the latest copy of every source fetched by URL,
	// one folder per rule set. The panel manages it; users can only read.
	DownloadsBucket = "downloads"
)

// ErrManaged refuses changes to a bucket the panel fills itself.
var ErrManaged = apperr.New(403, "этот бакет ведёт панель: файлы в нём появляются, обновляются и удаляются сами")

var (
	ErrBucketNotFound = apperr.NotFound("бакет не найден")
	ErrObjectNotFound = apperr.NotFound("файл не найден")
	ErrBucketExists   = apperr.Conflict("бакет с таким именем уже есть")
	ErrBucketNotEmpty = apperr.Conflict("в бакете есть файлы — удалите их или включите удаление вместе с файлами")
	ErrTooLarge       = apperr.TooLarge("файл больше допустимого размера")
	bucketRe          = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$`)
)

type Service struct {
	db        *store.DB
	blobDir   string
	MaxUpload int64
}

func New(db *store.DB, dataDir string, maxUpload int64) (*Service, error) {
	dir := filepath.Join(dataDir, "blobs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &Service{db: db, blobDir: dir, MaxUpload: maxUpload}, nil
}

func ValidBucketName(name string) error {
	if !bucketRe.MatchString(name) {
		return apperr.Bad("имя бакета: 3–63 символа, строчная латиница, цифры и дефис, без дефиса по краям")
	}
	return nil
}

// CleanKey validates an object key. Keys look like relative paths.
func CleanKey(key string) (string, error) {
	key = strings.TrimSpace(key)
	if key == "" || len(key) > 512 || !utf8.ValidString(key) {
		return "", apperr.Bad("имя файла пустое или слишком длинное")
	}
	for _, r := range key {
		if unicode.IsControl(r) || r == '\\' {
			return "", apperr.Bad("имя файла содержит недопустимые символы")
		}
	}
	if strings.HasPrefix(key, "/") || strings.HasSuffix(key, "/") || strings.Contains(key, "//") {
		return "", apperr.Bad("путь не должен начинаться или заканчиваться на / и содержать //")
	}
	for _, seg := range strings.Split(key, "/") {
		if seg == "." || seg == ".." || strings.TrimSpace(seg) != seg {
			return "", apperr.Bad("путь содержит недопустимый сегмент")
		}
	}
	return key, nil
}

// CleanPrefix validates a folder prefix ("" or "a/b/").
func CleanPrefix(p string) (string, error) {
	if p == "" {
		return "", nil
	}
	if !strings.HasSuffix(p, "/") {
		p += "/"
	}
	if _, err := CleanKey(strings.TrimSuffix(p, "/")); err != nil {
		return "", err
	}
	return p, nil
}

func objKey(bucket, key string) string { return bucket + "/" + key }

// ---- buckets ---------------------------------------------------------------

func (s *Service) Buckets() ([]Bucket, error) {
	return store.List[Bucket](s.db, store.Buckets, "")
}

func (s *Service) Bucket(name string) (*Bucket, error) {
	var b Bucket
	if err := s.db.Get(store.Buckets, name, &b); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, ErrBucketNotFound
		}
		return nil, err
	}
	return &b, nil
}

func (s *Service) CreateBucket(name string, public bool) (*Bucket, error) {
	if err := ValidBucketName(name); err != nil {
		return nil, err
	}
	b := &Bucket{Name: name, Public: public, CreatedAt: time.Now().UTC()}
	ok, err := s.db.Insert(store.Buckets, name, b)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrBucketExists
	}
	return b, nil
}

// EnsureSystemBucket creates a private bucket used by the panel itself.
func (s *Service) EnsureSystemBucket(name string) error {
	_, err := s.db.Insert(store.Buckets, name, &Bucket{Name: name, System: true, CreatedAt: time.Now().UTC()})
	return err
}

// EnsureManagedBucket creates a private, read-only bucket the panel fills.
func (s *Service) EnsureManagedBucket(name string) error {
	if _, err := s.db.Insert(store.Buckets, name, &Bucket{Name: name, System: true, Managed: true, CreatedAt: time.Now().UTC()}); err != nil {
		return err
	}
	_, err := store.Modify(s.db, store.Buckets, name, func(b *Bucket) error {
		b.System, b.Managed, b.Public = true, true, false
		return nil
	})
	return err
}

// Writable refuses user changes to managed buckets.
func (s *Service) Writable(bucket string) error {
	b, err := s.Bucket(bucket)
	if err != nil {
		return err
	}
	if b.Managed {
		return ErrManaged
	}
	return nil
}

func (s *Service) SetBucketPublic(name string, public bool) (*Bucket, error) {
	b, err := store.Modify(s.db, store.Buckets, name, func(b *Bucket) error {
		if b.System && public {
			return apperr.Bad("служебный бакет нельзя сделать публичным")
		}
		b.Public = public
		return nil
	})
	if errors.Is(err, store.ErrNotFound) {
		return nil, ErrBucketNotFound
	}
	return b, err
}

func (s *Service) DeleteBucket(name string, force bool) error {
	b, err := s.Bucket(name)
	if err != nil {
		return err
	}
	if b.System {
		return apperr.Bad("служебный бакет нельзя удалить")
	}
	objs, err := s.scan(name, "")
	if err != nil {
		return err
	}
	if len(objs) > 0 && !force {
		return ErrBucketNotEmpty
	}
	if _, err := s.DeleteObjects(name, nil, []string{""}); err != nil {
		return err
	}
	return s.db.Delete(store.Buckets, name)
}

type BucketStats struct {
	Objects int   `json:"objects"`
	Bytes   int64 `json:"bytes"`
}

func (s *Service) Stats(bucket string) BucketStats {
	var st BucketStats
	objs, _ := s.scan(bucket, "")
	for _, o := range objs {
		st.Objects++
		st.Bytes += o.Size
	}
	return st
}

// ---- objects ---------------------------------------------------------------

func (s *Service) scan(bucket, prefix string) ([]Object, error) {
	var out []Object
	err := s.db.Scan(store.Objects, objKey(bucket, prefix), func(_ string, raw []byte) error {
		var r objectRecord
		if err := json.Unmarshal(raw, &r); err != nil {
			return err
		}
		out = append(out, r.toObject())
		return nil
	})
	return out, err
}

// Walk returns every object of a bucket, in key order.
func (s *Service) Walk(bucket string) ([]Object, error) {
	if _, err := s.Bucket(bucket); err != nil {
		return nil, err
	}
	return s.scan(bucket, "")
}

type Listing struct {
	Prefix   string   `json:"prefix"`
	Folders  []string `json:"folders"`
	Objects  []Object `json:"objects"`
	Total    int      `json:"total"`
	TotalLen int64    `json:"totalBytes"`
}

// List returns objects directly under prefix plus the sub-folders, like
// S3 ListObjectsV2 with delimiter "/".
func (s *Service) List(bucket, prefix string) (*Listing, error) {
	if _, err := s.Bucket(bucket); err != nil {
		return nil, err
	}
	prefix, err := CleanPrefix(prefix)
	if err != nil {
		return nil, err
	}
	objs, err := s.scan(bucket, prefix)
	if err != nil {
		return nil, err
	}
	l := &Listing{Prefix: prefix, Folders: []string{}, Objects: []Object{}}
	seen := map[string]bool{}
	for _, o := range objs {
		l.Total++
		l.TotalLen += o.Size
		rest := strings.TrimPrefix(o.Key, prefix)
		if i := strings.IndexByte(rest, '/'); i >= 0 {
			f := prefix + rest[:i+1]
			if !seen[f] {
				seen[f] = true
				l.Folders = append(l.Folders, f)
			}
			continue
		}
		l.Objects = append(l.Objects, o)
	}
	sort.Strings(l.Folders)
	return l, nil
}

func (s *Service) Object(bucket, key string) (*Object, error) {
	var r objectRecord
	if err := s.db.Get(store.Objects, objKey(bucket, key), &r); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, ErrObjectNotFound
		}
		return nil, err
	}
	o := r.toObject()
	return &o, nil
}

// Put stores the content of r under bucket/key, replacing any old version.
func (s *Service) Put(bucket, key string, r io.Reader, contentType string) (*Object, error) {
	return s.put(bucket, key, r, contentType, "", s.MaxUpload)
}

// PutDownloaded stores a file the panel fetched from origin. Its size is
// already bounded by the fetch limit.
func (s *Service) PutDownloaded(bucket, key string, data []byte, origin string) (*Object, error) {
	return s.put(bucket, key, bytes.NewReader(data), "", origin, int64(len(data)))
}

func (s *Service) put(bucket, key string, r io.Reader, contentType, origin string, limit int64) (*Object, error) {
	if _, err := s.Bucket(bucket); err != nil {
		return nil, err
	}
	key, err := CleanKey(key)
	if err != nil {
		return nil, err
	}
	blobID, size, sum, sniff, err := s.writeBlob(r, limit)
	if err != nil {
		return nil, err
	}
	if contentType == "" {
		contentType = DetectContentType(key, sniff)
	}
	now := time.Now().UTC()
	rec := objectRecord{
		Object: Object{Bucket: bucket, Key: key, Size: size, ContentType: contentType, SHA256: sum, CreatedAt: now, UpdatedAt: now, Origin: origin},
		Blob:   blobID,
	}
	oldBlob := ""
	err = store.Upsert(s.db, store.Objects, objKey(bucket, key), func(old *objectRecord) (*objectRecord, error) {
		if old != nil {
			// A new version keeps the link, so it keeps the link's counts too.
			rec.CreatedAt, rec.Downloads = old.CreatedAt, old.Downloads
			oldBlob = old.Blob
		}
		return &rec, nil
	})
	if err != nil {
		s.removeBlob(blobID)
		return nil, err
	}
	if oldBlob != "" && oldBlob != blobID {
		s.removeBlob(oldBlob)
	}
	o := rec.toObject()
	return &o, nil
}

// AddDownloads adds fetch counts to objects keyed "bucket/key". Objects
// deleted in the meantime are skipped.
func (s *Service) AddDownloads(delta map[string]store.Counter) error {
	keys := make([]string, 0, len(delta))
	for k := range delta {
		keys = append(keys, k)
	}
	return store.ModifyEach(s.db, store.Objects, keys, func(k string, r *objectRecord) {
		r.Downloads.Add(delta[k])
	})
}

// ObjectKey is the key AddDownloads expects.
func ObjectKey(bucket, key string) string { return objKey(bucket, key) }

// Rename moves an object to a new key within the same bucket.
func (s *Service) Rename(bucket, from, to string) (*Object, error) {
	to, err := CleanKey(to)
	if err != nil {
		return nil, err
	}
	r, moved, err := store.Rekey(s.db, store.Objects, objKey(bucket, from), objKey(bucket, to), func(r *objectRecord) {
		r.Key = to
		r.UpdatedAt = time.Now().UTC()
	})
	if errors.Is(err, store.ErrNotFound) {
		return nil, ErrObjectNotFound
	}
	if err != nil {
		return nil, err
	}
	if !moved {
		return nil, apperr.Bad("файл с таким именем уже есть")
	}
	o := r.toObject()
	return &o, nil
}

// DeleteObjects removes single keys and whole folders (prefixes).
func (s *Service) DeleteObjects(bucket string, keys, prefixes []string) (int, error) {
	n := 0
	for _, k := range keys {
		var r objectRecord
		if err := s.db.Get(store.Objects, objKey(bucket, k), &r); err != nil {
			continue
		}
		if err := s.db.Delete(store.Objects, objKey(bucket, k)); err != nil {
			return n, err
		}
		s.removeBlob(r.Blob)
		n++
	}
	for _, p := range prefixes {
		if p != "" {
			var err error
			if p, err = CleanPrefix(p); err != nil {
				return n, err
			}
		}
		removed, err := s.db.DeletePrefix(store.Objects, objKey(bucket, p))
		if err != nil {
			return n, err
		}
		for _, raw := range removed {
			var r objectRecord
			if json.Unmarshal(raw, &r) == nil {
				s.removeBlob(r.Blob)
			}
			n++
		}
	}
	return n, nil
}

// Open returns the content of an object.
func (s *Service) Open(o *Object) (*os.File, error) {
	p, err := s.blobPath(o.BlobID)
	if err != nil {
		return nil, err
	}
	return os.Open(p)
}

// ReadAll loads a whole object into memory, refusing anything over max.
func (s *Service) ReadAll(o *Object, max int64) ([]byte, error) {
	if o.Size > max {
		return nil, ErrTooLarge
	}
	f, err := s.Open(o)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, max))
}

// TotalBytes sums every stored object.
func (s *Service) TotalBytes() (int, int64) {
	n, total := 0, int64(0)
	_ = s.db.Scan(store.Objects, "", func(_ string, raw []byte) error {
		var r objectRecord
		if json.Unmarshal(raw, &r) == nil {
			n++
			total += r.Size
		}
		return nil
	})
	return n, total
}

// ---- blobs -----------------------------------------------------------------

func (s *Service) blobPath(id string) (string, error) {
	if !store.IsID(id) {
		return "", errors.New("invalid blob id")
	}
	return filepath.Join(s.blobDir, id[:2], id), nil
}

func (s *Service) writeBlob(r io.Reader, limit int64) (id string, size int64, sum string, sniff []byte, err error) {
	id = store.NewID()
	final, _ := s.blobPath(id)
	if err = os.MkdirAll(filepath.Dir(final), 0o700); err != nil {
		return
	}
	tmp, err := os.CreateTemp(s.blobDir, ".upload-*")
	if err != nil {
		return
	}
	defer func() {
		if err != nil {
			tmp.Close()
			os.Remove(tmp.Name())
		}
	}()
	h := sha256.New()
	head := &headCapture{max: 512}
	n, err := io.Copy(io.MultiWriter(tmp, h, head), io.LimitReader(r, limit+1))
	if err != nil {
		return
	}
	if n > limit {
		err = ErrTooLarge
		return
	}
	if err = tmp.Sync(); err != nil {
		return
	}
	if err = tmp.Close(); err != nil {
		return
	}
	if err = os.Rename(tmp.Name(), final); err != nil {
		return
	}
	return id, n, hex.EncodeToString(h.Sum(nil)), head.buf, nil
}

func (s *Service) removeBlob(id string) {
	if p, err := s.blobPath(id); err == nil {
		os.Remove(p)
	}
}

type headCapture struct {
	buf []byte
	max int
}

func (h *headCapture) Write(p []byte) (int, error) {
	if room := h.max - len(h.buf); room > 0 {
		if len(p) < room {
			room = len(p)
		}
		h.buf = append(h.buf, p[:room]...)
	}
	return len(p), nil
}

// ---- content types ---------------------------------------------------------

var knownTypes = map[string]string{
	".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".gif": "image/gif",
	".webp": "image/webp", ".avif": "image/avif", ".svg": "image/svg+xml", ".ico": "image/x-icon",
	".css": "text/css; charset=utf-8", ".js": "text/javascript; charset=utf-8", ".mjs": "text/javascript; charset=utf-8",
	".json": "application/json", ".html": "text/html; charset=utf-8", ".htm": "text/html; charset=utf-8",
	".xml": "application/xml", ".txt": "text/plain; charset=utf-8", ".list": "text/plain; charset=utf-8",
	".conf": "text/plain; charset=utf-8", ".md": "text/markdown; charset=utf-8", ".csv": "text/csv; charset=utf-8",
	".yaml": "text/yaml; charset=utf-8", ".yml": "text/yaml; charset=utf-8", ".mrs": "application/octet-stream",
	".woff2": "font/woff2", ".woff": "font/woff", ".ttf": "font/ttf", ".otf": "font/otf",
	".pdf": "application/pdf", ".zip": "application/zip", ".gz": "application/gzip",
	".mp4": "video/mp4", ".webm": "video/webm", ".mp3": "audio/mpeg", ".ogg": "audio/ogg",
}

// DetectContentType picks a type from the extension. Unknown files are
// sniffed only to tell text from binary; content is never upgraded to HTML.
func DetectContentType(key string, head []byte) string {
	ext := strings.ToLower(path.Ext(key))
	if t, ok := knownTypes[ext]; ok {
		return t
	}
	if t := mime.TypeByExtension(ext); strings.HasPrefix(t, "image/") && ext != ".svg" {
		return t
	}
	if strings.HasPrefix(http.DetectContentType(head), "text/") && utf8.Valid(head) {
		return "text/plain; charset=utf-8"
	}
	return "application/octet-stream"
}

func (o *Object) Name() string { return path.Base(o.Key) }

func (o *Object) String() string { return fmt.Sprintf("%s/%s", o.Bucket, o.Key) }
