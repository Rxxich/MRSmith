package server

import (
	"context"
	"net/http"
	"time"

	"mrsmith/internal/apperr"
	"mrsmith/internal/netproxy"
	"mrsmith/internal/notify"
)

// recordingWriter remembers the status a public file was answered with.
type recordingWriter struct {
	http.ResponseWriter
	status int
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
	return w.ResponseWriter.Write(b)
}

func (w *recordingWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// counted reports whether a public response counts as a download (200) or
// as a check of an up-to-date copy (304). HEAD and range requests do not count.
func counted(r *http.Request, status int) (ok, notModified bool) {
	if r.Method != http.MethodGet {
		return false, false
	}
	switch status {
	case http.StatusOK:
		return true, false
	case http.StatusNotModified:
		return true, true
	}
	return false, false
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
	sum, err := s.dl.Clients(300)
	if err != nil {
		return err
	}
	recent, err := s.dl.Recent(100)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"summary": sum, "recent": recent})
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
