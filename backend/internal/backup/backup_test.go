package backup

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"mrsmith/internal/fetch"
	"mrsmith/internal/rules"
	"mrsmith/internal/rulesets"
	"mrsmith/internal/storage"
	"mrsmith/internal/store"
)

type fakeTG struct {
	mu    sync.Mutex
	files []string
	texts []string
}

func (f *fakeTG) SendFile(_ context.Context, path, name, caption string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, err := os.Stat(path); err != nil {
		return err
	}
	f.files = append(f.files, name+"|"+caption)
	return nil
}

func (f *fakeTG) Send(_ context.Context, html string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.texts = append(f.texts, html)
	return nil
}

type env struct {
	t      *testing.T
	db     *store.DB
	st     *storage.Service
	mgr    *rulesets.Manager
	svc    *Service
	dir    string
	data   string
	src    *httptest.Server
	tg     *fakeTG
	srcHit func() int
}

func newEnv(t *testing.T) *env {
	t.Helper()
	data, dir := t.TempDir(), t.TempDir()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	db, err := store.Open(filepath.Join(data, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	st, _ := storage.New(db, data, 5<<20)
	var mu sync.Mutex
	hits := 0
	src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
		io.WriteString(w, "+.twitch.tv\nttvnw.net\n")
	}))
	t.Cleanup(src.Close)
	f := fetch.New(5*time.Second, 5<<20, true, nil, "test")
	mgr, err := rulesets.NewManager(db, st, f, data, 5<<20, log)
	if err != nil {
		t.Fatal(err)
	}
	tg := &fakeTG{}
	svc := New(context.Background(), Options{Dir: dir, Version: "test"}, db, mgr, st, tg, log)
	e := &env{t: t, db: db, st: st, mgr: mgr, svc: svc, dir: dir, data: data, src: src, tg: tg}
	e.srcHit = func() int { mu.Lock(); defer mu.Unlock(); return hits }
	t.Cleanup(svc.Wait)
	return e
}

func (e *env) set(in rulesets.Input) *rulesets.Ruleset {
	e.t.Helper()
	rs, err := e.mgr.Create(in)
	if err != nil {
		e.t.Fatal(err)
	}
	rs, _, err = e.mgr.Refresh(context.Background(), rs.ID, true, "create")
	if err != nil || rs.State.Status != rulesets.StatusOK {
		e.t.Fatalf("build %s: %v %+v", in.Name, err, rs.State)
	}
	return rs
}

func (e *env) create(linkBase string) string {
	e.t.Helper()
	if err := e.svc.Create(linkBase); err != nil {
		e.t.Fatal(err)
	}
	e.svc.Wait()
	st := e.svc.Status()
	if st.Last == nil || st.Last.Error != "" || st.Last.Name == "" {
		e.t.Fatalf("create: %+v", st.Last)
	}
	return st.Last.Name
}

func (e *env) restore(name, linkBase string) *Job {
	e.t.Helper()
	if err := e.svc.Restore(name, linkBase); err != nil {
		e.t.Fatal(err)
	}
	e.svc.Wait()
	return e.svc.Status().Last
}

func blobCount(t *testing.T, data string) int {
	n := 0
	filepath.WalkDir(filepath.Join(data, "blobs"), func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && !strings.HasPrefix(d.Name(), ".") {
			n++
		}
		return nil
	})
	return n
}

func TestBackupAndRestore(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if _, err := e.st.CreateBucket("assets", true); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.Put("assets", "lists/game.txt", strings.NewReader("+.game.example\n"), ""); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.Put(storage.SourcesBucket, "upload/ips.txt", strings.NewReader("149.154.160.0/20\n"), ""); err != nil {
		t.Fatal(err)
	}
	oldBase := e.src.URL
	twitch := e.set(rulesets.Input{
		Name: "Twitch", Formats: []rules.OutputFormat{rules.OutMRS, rules.OutYAML}, Private: true,
		Sources: []rulesets.SourceInput{{Type: rulesets.SourceURL, URL: oldBase + "/f/assets/lists/game.txt"}},
		Include: []string{"+.extra.example"}, Exclude: []rules.Exclusion{{Rule: "ttvnw.net"}},
	})
	tg := e.set(rulesets.Input{
		Name: "Telegram IP", Formats: []rules.OutputFormat{rules.OutMRS},
		Sources: []rulesets.SourceInput{{Type: rulesets.SourceObject, Bucket: storage.SourcesBucket, Key: "upload/ips.txt"}},
	})
	if _, err := e.mgr.ReplaceEntry(ctx, twitch.ID, "+.extra.example", "+.edited.example"); err != nil {
		t.Fatal(err)
	}
	twitch, _ = e.mgr.Get(twitch.ID)

	name := e.create(oldBase)
	if strings.Contains(name, "auto") || !strings.HasPrefix(name, "mrsmith-") {
		t.Fatalf("manual archive named %q", name)
	}
	files := e.svc.Status().Files
	if len(files) != 1 || files[0].Manifest == nil || files[0].Manifest.Rulesets != 2 || files[0].Foreign || files[0].Auto {
		t.Fatalf("list: %+v", files)
	}
	if m := files[0].Manifest; m.LinkBase != oldBase || m.Objects < 3 || !slices.Contains(m.Names, "Twitch") {
		t.Fatalf("manifest: %+v", m)
	}

	// Change everything after the backup.
	if err := e.mgr.Delete(tg.ID); err != nil {
		t.Fatal(err)
	}
	extra := e.set(rulesets.Input{Name: "Later", Formats: []rules.OutputFormat{rules.OutYAML}, Include: []string{"+.later.example"}})
	if _, err := e.mgr.EditEntries(ctx, twitch.ID, []string{"+.after.example"}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.Put("assets", "lists/game.txt", strings.NewReader("changed\n"), ""); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.Put("assets", "new.txt", strings.NewReader("new\n"), ""); err != nil {
		t.Fatal(err)
	}

	newBase := strings.Replace(oldBase, "127.0.0.1", "localhost", 1)
	hitsBefore := e.srcHit()
	job := e.restore(name, newBase)
	if job == nil || job.Error != "" || job.Restore == nil {
		t.Fatalf("restore: %+v", job)
	}
	r := job.Restore
	if r.Rulesets != 2 || r.Rebased != 1 || r.OldBase != oldBase || r.NewBase != newBase || r.Safety == "" {
		t.Fatalf("result: %+v", r)
	}
	if e.srcHit() != hitsBefore {
		t.Fatal("rebuild after restore fetched sources it had copies of")
	}

	got, _ := e.mgr.Get(twitch.ID)
	if got == nil || got.Token != twitch.Token || got.Slug != twitch.Slug ||
		!slices.Equal(got.Include, []string{"+.edited.example"}) || len(got.Exclude) != 1 {
		t.Fatalf("twitch after restore: %+v", got)
	}
	if want := newBase + "/f/assets/lists/game.txt"; got.Sources[0].URL != want {
		t.Fatalf("source %q, want %q", got.Sources[0].URL, want)
	}
	if got.State.Status != rulesets.StatusOK || got.State.InputHash == "" {
		t.Fatalf("twitch was not rebuilt: %+v", got.State)
	}
	if _, err := os.Stat(e.mgr.OutputPath(got.ID, rules.OutMRS)); err != nil {
		t.Fatal("no built file after restore:", err)
	}
	if back, err := e.mgr.Get(tg.ID); err != nil || back.State.Status != rulesets.StatusOK {
		t.Fatalf("deleted set did not come back: %v %+v", err, back)
	}
	if _, err := e.mgr.Get(extra.ID); err == nil {
		t.Fatal("set made after the backup survived the restore")
	}
	if rs, err := e.mgr.BySlug(twitch.Slug); err != nil || rs.ID != twitch.ID {
		t.Fatal("public link index not refreshed")
	}

	o, err := e.st.Object("assets", "lists/game.txt")
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := e.st.ReadAll(o, 1<<20); string(b) != "+.game.example\n" {
		t.Fatalf("file content %q", b)
	}
	if _, err := e.st.Object("assets", "new.txt"); err == nil {
		t.Fatal("file made after the backup survived")
	}
	if b, _ := e.st.Bucket("assets"); b == nil || !b.Public {
		t.Fatal("bucket settings lost")
	}
	if b, _ := e.st.Bucket(storage.DownloadsBucket); b == nil || !b.Managed {
		t.Fatal("downloads bucket lost its flags")
	}
	n, _ := e.st.TotalBytes()
	if c := blobCount(t, e.data); c != n {
		t.Fatalf("%d blobs on disk for %d objects: old content not cleaned up", c, n)
	}

	// The safety copy holds what was there before.
	safety := filepath.Join(e.dir, r.Safety)
	m, err := readManifest(safety)
	if err != nil || m.Kind != KindBeforeRestore || !slices.Contains(m.Names, "Later") {
		t.Fatalf("safety archive: %v %+v", err, m)
	}
	// And restoring it brings the later state back.
	job = e.restore(r.Safety, newBase)
	if job.Error != "" {
		t.Fatal(job.Error)
	}
	if _, err := e.mgr.Get(extra.ID); err != nil {
		t.Fatal("restore of the safety copy failed:", err)
	}
}

func TestRestoreRefusesDamagedArchive(t *testing.T) {
	e := newEnv(t)
	if _, err := e.st.Put(storage.SourcesBucket, "a.txt", strings.NewReader("+.a.example\n"), ""); err != nil {
		t.Fatal(err)
	}
	rs := e.set(rulesets.Input{Name: "A", Formats: []rules.OutputFormat{rules.OutYAML},
		Sources: []rulesets.SourceInput{{Type: rulesets.SourceObject, Bucket: storage.SourcesBucket, Key: "a.txt"}}})
	name := e.create("")

	// Same length, other bytes: the zip checksum and the hash both catch it.
	zr, err := zip.OpenReader(filepath.Join(e.dir, name))
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range zr.File {
		rc, _ := f.Open()
		data, _ := io.ReadAll(rc)
		rc.Close()
		if strings.HasPrefix(f.Name, filesDir) {
			data = bytes.Repeat([]byte("x"), len(data))
		}
		w, _ := zw.Create(f.Name)
		w.Write(data)
	}
	zw.Close()
	zr.Close()
	os.WriteFile(filepath.Join(e.dir, "damaged.zip"), buf.Bytes(), 0o600)
	os.WriteFile(filepath.Join(e.dir, "junk.zip"), []byte("not a zip"), 0o600)

	if err := e.svc.Restore("junk.zip", ""); err == nil {
		t.Fatal("junk accepted")
	}
	for _, f := range e.svc.Status().Files {
		if f.Name == "junk.zip" && f.Error == "" {
			t.Fatal("junk listed without an error")
		}
	}
	blobs := blobCount(t, e.data)
	job := e.restore("damaged.zip", "")
	if job.Error == "" || !strings.Contains(job.Error, "повреждён") {
		t.Fatalf("damaged archive restored: %+v", job)
	}
	if got, err := e.mgr.Get(rs.ID); err != nil || got.State.Status != rulesets.StatusOK {
		t.Fatal("data changed by a failed restore")
	}
	if c := blobCount(t, e.data); c != blobs {
		t.Fatalf("failed restore left %d blobs, had %d", c, blobs)
	}
	for _, p := range []string{"../x.zip", "a/b.zip", ".hidden.zip", "x.txt", "c:x.zip"} {
		if validName(p) {
			t.Errorf("%q accepted", p)
		}
	}
	if !validName("mrsmith-2026-11-23-1530 (1).zip") {
		t.Error("browser copy name refused")
	}
}

func TestScheduleRotationAndTelegram(t *testing.T) {
	e := newEnv(t)
	e.set(rulesets.Input{Name: "A", Formats: []rules.OutputFormat{rules.OutYAML}, Include: []string{"+.a.example"}})
	os.WriteFile(filepath.Join(e.dir, "brought-over-auto.zip"), []byte("x"), 0o600)
	manual := e.create("https://rules.example.com")

	if err := e.svc.SetSettings(Settings{EveryHours: 12, Keep: 2, Telegram: true}, ""); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 23, 3, 0, 0, 0, time.UTC)
	e.svc.now = func() time.Time { return now }
	var autos []string
	for i := 0; i < 4; i++ {
		e.svc.tick()
		e.svc.Wait()
		e.svc.tick() // not due yet: no second archive
		e.svc.Wait()
		last := e.svc.Status().Last
		if last.Error != "" || last.Kind != KindAuto {
			t.Fatalf("auto backup %d: %+v", i, last)
		}
		autos = append(autos, last.Name)
		now = now.Add(13 * time.Hour)
	}
	st := e.svc.Status()
	var names []string
	for _, f := range st.Files {
		names = append(names, f.Name)
	}
	for _, keep := range []string{autos[2], autos[3], manual, "brought-over-auto.zip"} {
		if !slices.Contains(names, keep) {
			t.Fatalf("%s removed; left %v", keep, names)
		}
	}
	for _, gone := range autos[:2] {
		if slices.Contains(names, gone) {
			t.Fatalf("old %s kept; left %v", gone, names)
		}
	}
	if len(e.tg.files) != 4 || !strings.Contains(e.tg.files[0], "1 набор правил") {
		t.Fatalf("telegram: %v", e.tg.files)
	}
	for _, f := range e.tg.files {
		if strings.Contains(f, "http") || strings.Contains(f, "docker-compose") {
			t.Fatalf("caption carries the address or the restore hint: %q", f)
		}
	}
	if !st.NextAutoAt.Equal(st.LastAutoAt.Add(12 * time.Hour)) {
		t.Fatalf("next %v after %v", st.NextAutoAt, st.LastAutoAt)
	}

	// Archives too big for Telegram go out as a note.
	e.svc.opt.TelegramMax = 10
	e.create("")
	if len(e.tg.texts) != 1 || !strings.Contains(e.tg.texts[0], "только в папке backup") {
		t.Fatalf("big archive: %v", e.tg.texts)
	}
	if w := e.svc.Status().Last.Warning; !strings.Contains(w, "Telegram") {
		t.Fatalf("warning %q", w)
	}
	if err := e.svc.SetSettings(Settings{EveryHours: 5, Keep: 2}, ""); err == nil {
		t.Fatal("odd interval accepted")
	}
}

func TestMissingFolder(t *testing.T) {
	e := newEnv(t)
	e.svc.opt.Dir = filepath.Join(e.dir, "absent")
	st := e.svc.Status()
	if st.Ready || !strings.Contains(st.Problem, "./backup:/backup") {
		t.Fatalf("status %+v", st)
	}
	if err := e.svc.Create(""); err == nil {
		t.Fatal("backup into a missing folder")
	}
}

func TestRebaser(t *testing.T) {
	r := rebaser("https://old.example", "https://new.example")
	for in, want := range map[string]string{
		"https://old.example/r/twitch.mrs?token=abc": "https://new.example/r/twitch.mrs?token=abc",
		"https://OLD.example/f/assets/a.txt":         "https://new.example/f/assets/a.txt",
		"https://old.example/api/x":                  "",
		"https://old.example.org/r/x.mrs":            "",
		"https://github.com/r/x":                     "",
	} {
		got, ok := r(in)
		if ok != (want != "") || got != want {
			t.Errorf("%s: %q %v, want %q", in, got, ok, want)
		}
	}
	if _, ok := rebaser("https://same.example", "https://same.example")("https://same.example/r/x.mrs"); ok {
		t.Error("same address rewritten")
	}
	if cleanBase("https://Rules.Example.com/") != "https://rules.example.com" || cleanBase("javascript:alert(1)") != "" {
		t.Error("cleanBase")
	}
}

// The first automatic archive is made on the first tick after the schedule
// is switched on, with a clock that moves between readings as it does on a
// Linux server (on Windows two readings in a row are often equal).
func TestFirstAutoBackupWithMovingClock(t *testing.T) {
	e := newEnv(t)
	e.set(rulesets.Input{Name: "A", Formats: []rules.OutputFormat{rules.OutYAML}, Include: []string{"+.a.example"}})
	if err := e.svc.SetSettings(Settings{EveryHours: 24, Keep: 5}, ""); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	clock := time.Now()
	e.svc.now = func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		clock = clock.Add(time.Microsecond)
		return clock
	}
	e.svc.tick()
	e.svc.Wait()
	st := e.svc.Status()
	if st.Last == nil || st.Last.Kind != KindAuto || st.Last.Error != "" || st.LastAutoAt.IsZero() {
		t.Fatalf("first tick made no automatic backup: %+v", st.Last)
	}
	if d := st.NextAutoAt.Sub(st.LastAutoAt); d != 24*time.Hour {
		t.Fatalf("next automatic backup in %v", d)
	}
}
