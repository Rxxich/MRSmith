// Package backup saves rule sets (with manual edits) and the file storage
// into zip archives in a folder next to docker-compose.yml, removes old
// automatic ones, and restores the panel from any archive in that folder.
package backup

import (
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"io/fs"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"mrsmith/internal/apperr"
	"mrsmith/internal/rulesets"
	"mrsmith/internal/storage"
	"mrsmith/internal/store"
)

// Telegram delivers archives to the admins.
type Telegram interface {
	SendFile(ctx context.Context, path, name, caption string) error
	Send(ctx context.Context, html string) error
}

type Options struct {
	Dir       string // the folder with archives, /backup in the container
	PublicURL string // MRSMITH_PUBLIC_URL; wins over the address seen in the browser
	Version   string
	// TelegramMax is the largest archive sent to Telegram.
	TelegramMax int64
}

// Settings are what the admin picks in the panel.
type Settings struct {
	EveryHours int  `json:"everyHours"` // 0 is off
	Keep       int  `json:"keep"`       // automatic archives to keep
	Telegram   bool `json:"telegram"`
}

var (
	everyChoices = []int{0, 12, 24, 72, 168}
	keepMin      = 1
	keepMax      = 30
	retryAfter   = 30 * time.Minute
	metaKey      = "backup"
)

// meta is kept in the database, so it survives restarts and is never part
// of an archive.
type meta struct {
	Settings
	// LinkBase is the panel address last seen in the browser, used when
	// MRSMITH_PUBLIC_URL is not set.
	LinkBase   string    `json:"linkBase,omitempty"`
	Server     string    `json:"server"`
	LastAutoAt time.Time `json:"lastAutoAt,omitzero"`
	LastTryAt  time.Time `json:"lastTryAt,omitzero"`
	LastError  string    `json:"lastError,omitempty"`
	// Auto lists automatic archives made here, oldest first. Only these are
	// removed when there are too many.
	Auto []string `json:"auto,omitempty"`
}

// Job is a running or finished operation on archives.
type Job struct {
	Op         string         `json:"op"` // create | restore
	Kind       string         `json:"kind,omitempty"`
	Name       string         `json:"name,omitempty"`
	StartedAt  time.Time      `json:"startedAt"`
	FinishedAt time.Time      `json:"finishedAt,omitzero"`
	Error      string         `json:"error,omitempty"`
	Warning    string         `json:"warning,omitempty"`
	Restore    *RestoreResult `json:"restore,omitempty"`
}

type File struct {
	Name     string    `json:"name"`
	Size     int64     `json:"size"`
	ModTime  time.Time `json:"modTime"`
	Auto     bool      `json:"auto"`    // made by the schedule here; old ones are removed
	Foreign  bool      `json:"foreign"` // made by another panel
	Manifest *Manifest `json:"manifest,omitempty"`
	Error    string    `json:"error,omitempty"`
}

type Status struct {
	Dir        string    `json:"dir"`
	Ready      bool      `json:"ready"`
	Problem    string    `json:"problem,omitempty"`
	Settings   Settings  `json:"settings"`
	Telegram   bool      `json:"telegram"`
	LastAutoAt time.Time `json:"lastAutoAt,omitzero"`
	NextAutoAt time.Time `json:"nextAutoAt,omitzero"`
	LastError  string    `json:"lastError,omitempty"`
	Job        *Job      `json:"job"`
	Last       *Job      `json:"last"`
	Files      []File    `json:"files"`
}

type Service struct {
	opt Options
	db  *store.DB
	rs  *rulesets.Manager
	st  *storage.Service
	tg  Telegram
	log *slog.Logger
	bg  context.Context
	now func() time.Time

	metaMu sync.Mutex
	mu     sync.Mutex
	job    *Job
	last   *Job
	done   chan struct{}  // closed when the running job ends
	wg     sync.WaitGroup // rebuilds after a restore
}

// New prepares the service. tg may be nil. ctx bounds background work.
func New(ctx context.Context, opt Options, db *store.DB, rs *rulesets.Manager, st *storage.Service, tg Telegram, log *slog.Logger) *Service {
	if opt.TelegramMax == 0 {
		opt.TelegramMax = 50 << 20
	}
	opt.PublicURL = cleanBase(opt.PublicURL)
	return &Service{opt: opt, db: db, rs: rs, st: st, tg: tg, log: log, bg: ctx, now: time.Now}
}

// ---- folder ----------------------------------------------------------------

var (
	errNoFolder = apperr.Bad("Папка для бэкапов не подключена. Добавьте в docker-compose.yml, в volumes панели, строку «- ./backup:/backup» " +
		"(или возьмите docker-compose.yml из репозитория) и выполните docker compose up -d.")
	errNoWrite = apperr.Bad("Панель не может записывать в папку backup: у неё другой владелец. Возьмите docker-compose.yml из репозитория " +
		"(он сам выдаёт права) или выполните на сервере в папке с docker-compose.yml: chown -R 65532:65532 backup")
	errBusy     = apperr.Conflict("с бэкапами уже идёт работа — дождитесь её окончания")
	errBadName  = apperr.Bad("неверное имя файла бэкапа")
	errNotFound = apperr.NotFound("такого файла в папке бэкапов нет — нажмите «Проверить папку»")
)

// folder checks that archives can be written.
func (s *Service) folder() error {
	if s.opt.Dir == "" {
		return errNoFolder
	}
	info, err := os.Stat(s.opt.Dir)
	if err != nil || !info.IsDir() {
		if err == nil || errors.Is(err, fs.ErrNotExist) {
			return errNoFolder
		}
		return apperr.Bad("папка для бэкапов недоступна: %v", err)
	}
	f, err := os.CreateTemp(s.opt.Dir, ".write-test-*")
	if err != nil {
		return errNoWrite
	}
	f.Close()
	os.Remove(f.Name())
	return nil
}

// validName accepts a plain file name ending in .zip: whatever a browser or
// Telegram saved, but nothing that could leave the folder.
func validName(n string) bool {
	if n == "" || len(n) > 200 || !utf8.ValidString(n) || strings.HasPrefix(n, ".") || !strings.HasSuffix(strings.ToLower(n), ".zip") {
		return false
	}
	for _, r := range n {
		if r == '/' || r == '\\' || r == ':' || unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func (s *Service) path(name string) (string, error) {
	if !validName(name) {
		return "", errBadName
	}
	p := filepath.Join(s.opt.Dir, name)
	info, err := os.Stat(p)
	if err != nil || !info.Mode().IsRegular() {
		return "", errNotFound
	}
	return p, nil
}

// ---- settings --------------------------------------------------------------

func (s *Service) meta() meta {
	s.metaMu.Lock()
	defer s.metaMu.Unlock()
	return s.metaLocked()
}

func (s *Service) metaLocked() meta {
	var m meta
	if err := s.db.Get(store.Meta, metaKey, &m); err != nil {
		m = meta{Settings: Settings{Keep: 5}}
	}
	if m.Server == "" {
		m.Server = store.RandomHex(8)
		_ = s.db.Put(store.Meta, metaKey, m)
	}
	if m.Keep < keepMin || m.Keep > keepMax {
		m.Keep = 5
	}
	return m
}

func (s *Service) updateMeta(fn func(m *meta)) meta {
	s.metaMu.Lock()
	defer s.metaMu.Unlock()
	m := s.metaLocked()
	fn(&m)
	if err := s.db.Put(store.Meta, metaKey, m); err != nil {
		s.log.Warn("save backup settings", "err", err)
	}
	return m
}

// SetSettings saves the schedule. linkBase is the panel address the browser
// uses, remembered for automatic archives.
func (s *Service) SetSettings(in Settings, linkBase string) error {
	if !slices.Contains(everyChoices, in.EveryHours) {
		return apperr.Bad("расписание: раз в 12 часов, раз в день, раз в 3 дня, раз в неделю или выключено")
	}
	if in.Keep < keepMin || in.Keep > keepMax {
		return apperr.Bad("хранить автоматических: от %d до %d", keepMin, keepMax)
	}
	if in.Telegram && s.tg == nil {
		return apperr.Bad("Telegram не настроен: задайте TELEGRAM_TOKEN и ADMIN_ID в .env и перезапустите панель")
	}
	s.updateMeta(func(m *meta) {
		if m.EveryHours != in.EveryHours {
			m.LastError, m.LastTryAt = "", time.Time{}
		}
		m.Settings = in
		if b := cleanBase(linkBase); b != "" {
			m.LinkBase = b
		}
		m.Auto = s.rotate(m.Auto, m.Keep)
	})
	return nil
}

// cleanBase turns an address into scheme://host[/path] or "".
func cleanBase(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return ""
	}
	return u.Scheme + "://" + strings.ToLower(u.Host) + strings.TrimRight(u.EscapedPath(), "/")
}

func (s *Service) linkBase(fromBrowser string, m meta) string {
	if s.opt.PublicURL != "" {
		return s.opt.PublicURL
	}
	if b := cleanBase(fromBrowser); b != "" {
		return b
	}
	return m.LinkBase
}

// ---- status ----------------------------------------------------------------

func (s *Service) Status() Status {
	m := s.meta()
	st := Status{Dir: s.opt.Dir, Settings: m.Settings, Telegram: s.tg != nil, LastAutoAt: m.LastAutoAt, LastError: m.LastError, Files: []File{}}
	if err := s.folder(); err != nil {
		st.Problem = err.Error()
	} else {
		st.Ready = true
	}
	if m.EveryHours > 0 {
		st.NextAutoAt = s.nextAuto(m)
	}
	s.mu.Lock()
	if s.job != nil {
		j := *s.job
		st.Job = &j
	}
	if s.last != nil {
		j := *s.last
		st.Last = &j
	}
	s.mu.Unlock()
	if st.Ready || s.opt.Dir != "" {
		st.Files = s.files(m)
	}
	return st
}

func (s *Service) nextAuto(m meta) time.Time {
	if m.LastAutoAt.IsZero() {
		next := s.now()
		if m.LastError != "" {
			next = m.LastTryAt.Add(retryAfter)
		}
		return next.UTC()
	}
	next := m.LastAutoAt.Add(time.Duration(m.EveryHours) * time.Hour)
	if m.LastError != "" && m.LastTryAt.After(m.LastAutoAt) {
		next = m.LastTryAt.Add(retryAfter)
	}
	return next.UTC()
}

func (s *Service) files(m meta) []File {
	out := []File{}
	entries, err := os.ReadDir(s.opt.Dir)
	if err != nil {
		return out
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !validName(name) {
			continue
		}
		info, err := e.Info()
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		f := File{Name: name, Size: info.Size(), ModTime: info.ModTime().UTC(), Auto: slices.Contains(m.Auto, name)}
		if man, err := readManifest(filepath.Join(s.opt.Dir, name)); err != nil {
			f.Error = err.Error()
		} else {
			f.Manifest = man
			f.Foreign = man.Server != m.Server
		}
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return fileTime(out[i]).After(fileTime(out[j])) })
	return out
}

func fileTime(f File) time.Time {
	if f.Manifest != nil {
		return f.Manifest.CreatedAt
	}
	return f.ModTime
}

// ---- jobs ------------------------------------------------------------------

// start runs fn in the background unless another job is running. fn fills
// its own outcome; j, which Status reads, changes only under the lock.
func (s *Service) start(j *Job, fn func(out *Job) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.job != nil {
		return errBusy
	}
	j.StartedAt = s.now().UTC()
	s.job = j
	done := make(chan struct{})
	s.done = done
	go func() {
		defer close(done)
		out := &Job{}
		err := fn(out)
		s.mu.Lock()
		defer s.mu.Unlock()
		if out.Name != "" {
			j.Name = out.Name
		}
		j.Warning, j.Restore = out.Warning, out.Restore
		j.FinishedAt = s.now().UTC()
		if err != nil {
			j.Error = err.Error()
		}
		s.last, s.job = j, nil
	}()
	return nil
}

// Wait blocks until the running job, if any, and the rebuilds it started
// end. Tests use it.
func (s *Service) Wait() {
	s.mu.Lock()
	done := s.done
	s.mu.Unlock()
	if done != nil {
		<-done
	}
	s.wg.Wait()
}

// Create starts a backup made by hand.
func (s *Service) Create(linkBase string) error {
	if err := s.folder(); err != nil {
		return err
	}
	if b := cleanBase(linkBase); b != "" {
		s.updateMeta(func(m *meta) { m.LinkBase = b })
	}
	return s.start(&Job{Op: "create", Kind: KindManual}, func(j *Job) error { return s.runCreate(j, KindManual, linkBase) })
}

func (s *Service) runCreate(j *Job, kind, linkBase string) error {
	name, man, err := s.create(kind, linkBase)
	if kind == KindAuto {
		s.updateMeta(func(m *meta) {
			m.LastTryAt = s.now().UTC()
			if err != nil {
				m.LastError = "Автоматический бэкап не создан: " + err.Error()
				return
			}
			m.LastAutoAt, m.LastError = m.LastTryAt, ""
			m.Auto = s.rotate(append(m.Auto, name), m.Keep)
		})
	}
	if err != nil {
		return err
	}
	j.Name = name
	if m := s.meta(); m.Telegram && s.tg != nil {
		if werr := s.sendTelegram(name, man); werr != nil {
			j.Warning = "Бэкап создан, но в Telegram не отправлен: " + werr.Error()
			s.log.Warn("backup to telegram", "file", name, "err", werr)
			if kind == KindAuto {
				s.updateMeta(func(m *meta) { m.LastError = j.Warning })
			}
		}
	}
	return nil
}

// Restore starts replacing the panel's data with an archive.
func (s *Service) Restore(name, linkBase string) error {
	p, err := s.path(name)
	if err != nil {
		return err
	}
	man, err := readManifest(p)
	if err != nil {
		return apperr.Bad("%s", err.Error())
	}
	if man.Format > formatVersion {
		return apperr.Bad("бэкап сделан более новой версией панели (%s) — обновите панель и попробуйте снова", man.Version)
	}
	return s.start(&Job{Op: "restore", Name: name}, func(j *Job) error {
		res, err := s.restore(s.bg, name, linkBase)
		j.Restore = res
		return err
	})
}

// rotate removes the oldest automatic archives beyond keep, and forgets
// names whose files are gone.
func (s *Service) rotate(names []string, keep int) []string {
	var left []string
	for _, n := range names {
		if _, err := os.Lstat(filepath.Join(s.opt.Dir, n)); err == nil && validName(n) {
			left = append(left, n)
		}
	}
	for len(left) > keep {
		if err := os.Remove(filepath.Join(s.opt.Dir, left[0])); err != nil && !errors.Is(err, fs.ErrNotExist) {
			s.log.Warn("remove old backup", "file", left[0], "err", err)
		} else {
			s.log.Info("old backup removed", "file", left[0])
		}
		left = left[1:]
	}
	return left
}

// ---- files -----------------------------------------------------------------

func (s *Service) busyWith(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.job != nil && s.job.Name == name
}

// Open returns an archive for download.
func (s *Service) Open(name string) (*os.File, error) {
	p, err := s.path(name)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(p)
	if err != nil {
		return nil, apperr.Bad("%s", notZip(err).Error())
	}
	return f, nil
}

func (s *Service) Delete(name string) error {
	p, err := s.path(name)
	if err != nil {
		return err
	}
	if s.busyWith(name) {
		return errBusy
	}
	if err := os.Remove(p); err != nil {
		return apperr.Bad("не удалось удалить файл: %v", err)
	}
	s.updateMeta(func(m *meta) { m.Auto = slices.DeleteFunc(m.Auto, func(n string) bool { return n == name }) })
	return nil
}

// Save puts an uploaded archive into the folder, keeping its name unless it
// is taken. Anything that is not an MRSmith backup is refused.
func (s *Service) Save(name string, r io.Reader, limit int64) (*File, error) {
	if err := s.folder(); err != nil {
		return nil, err
	}
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	if !validName(name) {
		return nil, apperr.Bad("нужен файл бэкапа .zip")
	}
	tmp, err := os.CreateTemp(s.opt.Dir, ".upload-*.tmp")
	if err != nil {
		return nil, errNoWrite
	}
	ok := false
	defer func() {
		if !ok {
			tmp.Close()
			os.Remove(tmp.Name())
		}
	}()
	n, err := io.Copy(tmp, io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if n > limit {
		return nil, apperr.TooLarge(fmt.Sprintf("файл больше %d МБ — положите его в папку backup на сервере и нажмите «Проверить папку»", limit>>20))
	}
	if err := tmp.Close(); err != nil {
		return nil, err
	}
	man, err := readManifest(tmp.Name())
	if err != nil {
		return nil, apperr.Bad("%s", err.Error())
	}
	final := name
	base := strings.TrimSuffix(name, filepath.Ext(name))
	for i := 2; ; i++ {
		if _, err := os.Lstat(filepath.Join(s.opt.Dir, final)); errors.Is(err, fs.ErrNotExist) {
			break
		}
		final = fmt.Sprintf("%s-%d.zip", base, i)
	}
	if err := os.Rename(tmp.Name(), filepath.Join(s.opt.Dir, final)); err != nil {
		return nil, err
	}
	ok = true
	m := s.meta()
	return &File{Name: final, Size: n, ModTime: s.now().UTC(), Manifest: man, Foreign: man.Server != m.Server}, nil
}

// ---- schedule --------------------------------------------------------------

// Run makes automatic archives on schedule until ctx ends.
func (s *Service) Run(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.tick()
		}
	}
}

func (s *Service) tick() {
	m := s.meta()
	if m.EveryHours <= 0 || s.now().Before(s.nextAuto(m)) {
		return
	}
	_ = s.start(&Job{Op: "create", Kind: KindAuto}, func(j *Job) error { return s.runCreate(j, KindAuto, "") })
}

// ---- telegram --------------------------------------------------------------

func (s *Service) sendTelegram(name string, man *Manifest) error {
	ctx, cancel := context.WithTimeout(s.bg, 20*time.Minute)
	defer cancel()
	p := filepath.Join(s.opt.Dir, name)
	info, err := os.Stat(p)
	if err != nil {
		return err
	}
	what := summary(man)
	if info.Size() > s.opt.TelegramMax {
		msg := fmt.Sprintf("<b>Бэкап MRSmith</b>\n%s\n\nФайл %s весит %s — Telegram принимает от ботов файлы до %s, поэтому он есть только в папке backup на сервере.",
			html.EscapeString(what), html.EscapeString(name), sizeText(info.Size()), sizeText(s.opt.TelegramMax))
		if err := s.tg.Send(ctx, msg); err != nil {
			return err
		}
		return fmt.Errorf("файл больше %s, отправлено только сообщение", sizeText(s.opt.TelegramMax))
	}
	caption := fmt.Sprintf("<b>Бэкап MRSmith</b>\n%s\n\nЧтобы восстановить, положите файл в папку backup рядом с docker-compose.yml и выберите его в настройках панели.",
		html.EscapeString(what))
	return s.tg.SendFile(ctx, p, name, caption)
}

func summary(m *Manifest) string {
	parts := []string{
		count(m.Rulesets, "набор правил", "набора правил", "наборов правил"),
		count(m.Objects, "файл", "файла", "файлов") + " в хранилище",
	}
	if m.LinkBase != "" {
		parts = append(parts, m.LinkBase)
	}
	return strings.Join(parts, ", ")
}

func count(n int, one, few, many string) string {
	form := many
	switch m10, m100 := n%10, n%100; {
	case m10 == 1 && m100 != 11:
		form = one
	case m10 >= 2 && m10 <= 4 && (m100 < 12 || m100 > 14):
		form = few
	}
	return fmt.Sprintf("%d %s", n, form)
}

func sizeText(n int64) string {
	switch {
	case n >= 1<<30:
		return strings.Replace(fmt.Sprintf("%.1f ГБ", float64(n)/(1<<30)), ".", ",", 1)
	case n >= 1<<20:
		return strings.Replace(fmt.Sprintf("%.1f МБ", float64(n)/(1<<20)), ".", ",", 1)
	case n >= 1<<10:
		return fmt.Sprintf("%d КБ", n>>10)
	}
	return fmt.Sprintf("%d Б", n)
}
