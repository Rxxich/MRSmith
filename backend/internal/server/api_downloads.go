package server

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"mrsmith/internal/apperr"
	"mrsmith/internal/downloads"
	"mrsmith/internal/netproxy"
	"mrsmith/internal/notify"
)

// recordingWriter remembers how a public file was answered: the status,
// how much of the body went out and whether writing failed.
type recordingWriter struct {
	http.ResponseWriter
	status int
	bytes  int64
	broken bool
}

func (w *recordingWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *recordingWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += int64(n)
	if err != nil {
		w.broken = true
	}
	return n, err
}

func (w *recordingWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// result tells what a public response amounts to. Only GET requests go into
// the journal; HEAD and range requests (206) do not.
func result(r *http.Request, w *recordingWriter) (downloads.Result, bool) {
	if r.Method != http.MethodGet {
		return downloads.Result{}, false
	}
	size, _ := strconv.ParseInt(w.Header().Get("Content-Length"), 10, 64)
	res := downloads.Result{Bytes: w.bytes, Size: size}
	switch {
	case w.status == http.StatusNotModified:
		res.Outcome, res.Bytes, res.Size = downloads.OutcomeNotModified, 0, 0
	case w.status == http.StatusOK && (w.broken || size > 0 && w.bytes < size):
		res.Outcome = downloads.OutcomeAborted
	case w.status == http.StatusOK:
		res.Outcome = downloads.OutcomeOK
	case w.status == http.StatusNotFound:
		res.Outcome, res.Bytes, res.Size = downloads.OutcomeMissing, 0, 0
	case w.status >= 500:
		res.Outcome, res.Bytes, res.Size = downloads.OutcomeError, 0, 0
	default:
		return res, false
	}
	return res, true
}

// failure is a request turned away before anything was sent.
func failure(r *http.Request, outcome string) (downloads.Result, bool) {
	return downloads.Result{Outcome: outcome}, r.Method == http.MethodGet
}

func (s *Server) flushDownloads() {
	if s.dl != nil {
		s.dl.Flush()
	}
}

func (s *Server) handleDownloads(w http.ResponseWriter, r *http.Request) error {
	if s.dl == nil {
		return apperr.NotFound("журнал скачиваний выключен")
	}
	s.dl.Flush()
	q := r.URL.Query()
	f := downloads.Filter{View: q.Get("view"), Query: q.Get("q"), Outcome: q.Get("outcome")}
	switch f.Outcome {
	case "", downloads.OutcomeOK, downloads.OutcomeNotModified, "failed":
	default:
		return badRequest("неизвестный результат запроса")
	}
	if len(f.Query) > 200 {
		return badRequest("слишком длинный запрос")
	}
	f.Offset, _ = strconv.Atoi(q.Get("offset"))
	f.Limit, _ = strconv.Atoi(q.Get("limit"))
	if f.Limit <= 0 || f.Limit > 200 {
		f.Limit = 50
	}
	page, err := s.dl.Query(f)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, page)
	return nil
}

func (s *Server) handleClearDownloads(w http.ResponseWriter, r *http.Request) error {
	if s.dl == nil {
		return apperr.NotFound("журнал скачиваний выключен")
	}
	if err := s.dl.Clear(); err != nil {
		return err
	}
	s.log.Info("download journal cleared", "username", userFrom(r).Username, "ip", s.clientIP(r))
	w.WriteHeader(http.StatusNoContent)
	return nil
}

type proxyView struct {
	URL       string `json:"url"`
	RemoteDNS bool   `json:"remoteDns"`
	Auth      bool   `json:"auth"`
}

func viewProxy(p *netproxy.Proxy) *proxyView {
	if p == nil {
		return nil
	}
	return &proxyView{URL: p.String(), RemoteDNS: p.RemoteDNS(), Auth: p.HasAuth()}
}

func (s *Server) handleIntegrations(w http.ResponseWriter, r *http.Request) error {
	type telegramView struct {
		Enabled bool       `json:"enabled"`
		Chats   int        `json:"chats"`
		Proxy   *proxyView `json:"proxy"`
	}
	tg := telegramView{}
	if s.tg != nil {
		tg = telegramView{Enabled: true, Chats: s.tg.Chats(), Proxy: viewProxy(s.tg.Proxy())}
	}
	var gh *proxyView
	if s.fetch != nil {
		gh = viewProxy(s.fetch.GitHubProxy())
	}
	writeJSON(w, http.StatusOK, map[string]any{"githubProxy": gh, "telegram": tg})
	return nil
}

func (s *Server) handleTelegramTest(w http.ResponseWriter, r *http.Request) error {
	if s.tg == nil {
		return badRequest("Telegram не настроен: задайте TELEGRAM_TOKEN и ADMIN_ID в .env и перезапустите панель")
	}
	ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
	defer cancel()
	if err := s.tg.Send(ctx, notify.TestMessage(r.Host)); err != nil {
		return apperr.New(http.StatusBadGateway, "%s", err.Error())
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}
