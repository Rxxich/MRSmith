package server

import (
	"context"
	"crypto/subtle"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"

	"mrsmith/internal/apperr"
	"mrsmith/internal/audit"
	"mrsmith/internal/auth"
	"mrsmith/internal/backup"
	"mrsmith/internal/config"
	"mrsmith/internal/downloads"
	"mrsmith/internal/fetch"
	"mrsmith/internal/notify"
	"mrsmith/internal/rulesets"
	"mrsmith/internal/storage"
)

type Deps struct {
	Config   *config.Config
	Auth     *auth.Service
	Rulesets *rulesets.Manager
	Storage  *storage.Service
	Audit    *audit.Service
	Fetch    *fetch.Client
	// Downloads counts public fetches; Telegram is nil when not configured.
	Downloads *downloads.Recorder
	Telegram  *notify.Telegram
	// Backups is nil when the build has none (tests).
	Backups *backup.Service
	Log     *slog.Logger
	Version string
	UI      fs.FS
	// Ctx bounds background work started by requests; cancel it on shutdown.
	Ctx context.Context
}

type Server struct {
	cfg     *config.Config
	auth    *auth.Service
	rs      *rulesets.Manager
	st      *storage.Service
	audit   *audit.Service
	fetch   *fetch.Client
	dl      *downloads.Recorder
	tg      *notify.Telegram
	bk      *backup.Service
	log     *slog.Logger
	version string
	bg      context.Context
	api     *http.ServeMux
	spa     *spa
	handler http.Handler
}

func New(d Deps) (*Server, error) {
	s := &Server{
		cfg: d.Config, auth: d.Auth, rs: d.Rulesets, st: d.Storage, audit: d.Audit, fetch: d.Fetch,
		dl: d.Downloads, tg: d.Telegram, bk: d.Backups, log: d.Log, version: d.Version, bg: d.Ctx, api: http.NewServeMux(),
	}
	var err error
	if s.spa, err = newSPA(d.UI, d.Config.PanelPath); err != nil {
		return nil, err
	}
	s.routes()
	s.handler = s.withBase(http.HandlerFunc(s.dispatch))
	return s, nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.handler.ServeHTTP(w, r) }

// dispatch splits public content (/r, /f) from the panel, which may live
// under a secret path.
func (s *Server) dispatch(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Path
	panel := s.cfg.PanelPath
	switch {
	case p == "/healthz":
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok\n"))
	case strings.HasPrefix(p, "/r/"):
		s.serveRuleset(w, r)
	case strings.HasPrefix(p, "/f/"):
		s.serveFile(w, r)
	case strings.HasPrefix(p, panel+"api/"):
		setAPIHeaders(w.Header())
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/" + strings.TrimPrefix(p, panel)
		r2.URL.RawPath = ""
		s.api.ServeHTTP(w, r2)
	case strings.HasPrefix(p, panel) || p+"/" == panel:
		setPanelHeaders(w.Header())
		s.spa.ServeHTTP(w, r)
	default:
		http.NotFound(w, r)
	}
}

type ctxKey int

const (
	ctxSession ctxKey = iota
	ctxUser
)

func sessionFrom(r *http.Request) *auth.Session { return r.Context().Value(ctxSession).(*auth.Session) }
func userFrom(r *http.Request) *auth.User       { return r.Context().Value(ctxUser).(*auth.User) }

func (s *Server) cookieName() string {
	if s.cfg.CookieSecure {
		return "__Host-mrsmith"
	}
	return "mrsmith_session"
}

// sameOriginRequest rejects state-changing requests from other sites using
// Fetch Metadata. Browsers that do not send it still face the CSRF token.
func sameOriginRequest(r *http.Request) bool {
	switch r.Header.Get("Sec-Fetch-Site") {
	case "", "same-origin", "none":
		return true
	}
	return false
}

func safeMethod(m string) bool { return m == http.MethodGet || m == http.MethodHead }

type handlerFunc func(w http.ResponseWriter, r *http.Request) error

// public wraps handlers that need no session (login).
func (s *Server) public(h handlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !safeMethod(r.Method) && (!sameOriginRequest(r) || r.Header.Get("X-Requested-With") != "MRSmith") {
			s.writeError(w, r, apperr.New(http.StatusForbidden, "запрос отклонён: неверный источник"))
			return
		}
		if err := h(w, r); err != nil {
			s.writeError(w, r, err)
		}
	}
}

// authed requires a valid session, and a CSRF token for unsafe methods.
func (s *Server) authed(h handlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(s.cookieName())
		if err != nil {
			s.writeError(w, r, auth.ErrNoSession)
			return
		}
		sess, user, err := s.auth.Authenticate(c.Value)
		if err != nil {
			s.clearCookie(w)
			s.writeError(w, r, err)
			return
		}
		if !safeMethod(r.Method) {
			tok := r.Header.Get("X-CSRF-Token")
			if !sameOriginRequest(r) || subtle.ConstantTimeCompare([]byte(tok), []byte(sess.CSRF)) != 1 {
				s.writeError(w, r, apperr.New(http.StatusForbidden, "запрос отклонён: устаревшая страница, обновите её"))
				return
			}
		}
		ctx := context.WithValue(r.Context(), ctxSession, sess)
		ctx = context.WithValue(ctx, ctxUser, user)
		if err := h(w, r.WithContext(ctx)); err != nil {
			s.writeError(w, r, err)
		}
	}
}

func (s *Server) setCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     s.cookieName(),
		Value:    token,
		Path:     "/",
		MaxAge:   int(s.cfg.SessionTTL.Seconds()),
		HttpOnly: true,
		Secure:   s.cfg.CookieSecure,
		SameSite: http.SameSiteStrictMode,
	})
}

func (s *Server) clearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: s.cookieName(), Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: s.cfg.CookieSecure, SameSite: http.SameSiteStrictMode,
	})
}

func (s *Server) routes() {
	a := s.api
	a.HandleFunc("POST /api/auth/login", s.public(s.handleLogin))
	a.HandleFunc("POST /api/auth/logout", s.authed(s.handleLogout))
	a.HandleFunc("GET /api/auth/me", s.authed(s.handleMe))
	a.HandleFunc("POST /api/auth/password", s.authed(s.handlePassword))
	a.HandleFunc("POST /api/auth/username", s.authed(s.handleUsername))
	a.HandleFunc("GET /api/auth/sessions", s.authed(s.handleSessions))
	a.HandleFunc("DELETE /api/auth/sessions/{id}", s.authed(s.handleRevokeSession))

	a.HandleFunc("GET /api/downloads", s.authed(s.handleDownloads))
	a.HandleFunc("DELETE /api/downloads", s.authed(s.handleClearDownloads))
	a.HandleFunc("GET /api/integrations", s.authed(s.handleIntegrations))
	a.HandleFunc("POST /api/integrations/telegram/test", s.authed(s.handleTelegramTest))

	a.HandleFunc("GET /api/backups", s.authed(s.handleBackups))
	a.HandleFunc("PUT /api/backups/settings", s.authed(s.handleBackupSettings))
	a.HandleFunc("POST /api/backups", s.authed(s.handleCreateBackup))
	a.HandleFunc("POST /api/backups/restore", s.authed(s.handleRestoreBackup))
	a.HandleFunc("POST /api/backups/upload", s.authed(s.handleUploadBackup))
	a.HandleFunc("GET /api/backups/files/{name}", s.authed(s.handleDownloadBackup))
	a.HandleFunc("DELETE /api/backups/files/{name}", s.authed(s.handleDeleteBackup))

	a.HandleFunc("GET /api/overview", s.authed(s.handleOverview))
	a.HandleFunc("GET /api/events", s.authed(s.handleEvents))

	a.HandleFunc("GET /api/rulesets", s.authed(s.handleRulesets))
	a.HandleFunc("POST /api/rulesets", s.authed(s.handleCreateRuleset))
	a.HandleFunc("POST /api/rulesets/sources/upload", s.authed(s.handleSourceUpload))
	a.HandleFunc("GET /api/rulesets/{id}", s.authed(s.handleRuleset))
	a.HandleFunc("PUT /api/rulesets/{id}", s.authed(s.handleUpdateRuleset))
	a.HandleFunc("DELETE /api/rulesets/{id}", s.authed(s.handleDeleteRuleset))
	a.HandleFunc("POST /api/rulesets/{id}/refresh", s.authed(s.handleRefreshRuleset))
	a.HandleFunc("GET /api/rulesets/{id}/entries", s.authed(s.handleEntries))
	a.HandleFunc("POST /api/rulesets/{id}/entries", s.authed(s.handleEditEntries))
	a.HandleFunc("POST /api/rulesets/{id}/entries/replace", s.authed(s.handleReplaceEntry))
	a.HandleFunc("POST /api/rulesets/{id}/overrides/clear", s.authed(s.handleClearOverrides))
	a.HandleFunc("POST /api/rulesets/{id}/exclude/preview", s.authed(s.handlePreviewExclusion))
	a.HandleFunc("POST /api/rulesets/{id}/token", s.authed(s.handleRotateToken))
	a.HandleFunc("GET /api/rulesets/{id}/events", s.authed(s.handleRulesetEvents))
	a.HandleFunc("GET /api/rulesets/{id}/download/{format}", s.authed(s.handleDownloadOutput))

	a.HandleFunc("POST /api/convert", s.authed(s.handleConvert))

	a.HandleFunc("GET /api/audit/candidates", s.authed(s.handleAuditCandidates))
	a.HandleFunc("GET /api/audit/lookup", s.authed(s.handleAuditLookup))
	a.HandleFunc("POST /api/audit", s.authed(s.handleAuditRun))
	a.HandleFunc("GET /api/audit/{id}", s.authed(s.handleAuditReport))
	a.HandleFunc("GET /api/audit/{id}/findings", s.authed(s.handleAuditFindings))
	a.HandleFunc("POST /api/audit/{id}/fix", s.authed(s.handleAuditFix))

	a.HandleFunc("GET /api/buckets", s.authed(s.handleBuckets))
	a.HandleFunc("POST /api/buckets", s.authed(s.handleCreateBucket))
	a.HandleFunc("PATCH /api/buckets/{name}", s.authed(s.handleUpdateBucket))
	a.HandleFunc("DELETE /api/buckets/{name}", s.authed(s.handleDeleteBucket))
	a.HandleFunc("GET /api/buckets/{name}/list", s.authed(s.handleListObjects))
	a.HandleFunc("POST /api/buckets/{name}/upload", s.authed(s.handleUpload))
	a.HandleFunc("GET /api/buckets/{name}/object", s.authed(s.handleGetObject))
	a.HandleFunc("POST /api/buckets/{name}/delete", s.authed(s.handleDeleteObjects))
	a.HandleFunc("POST /api/buckets/{name}/rename", s.authed(s.handleRenameObject))

	a.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusNotFound, errorBody{"нет такого метода API"})
	})
}
