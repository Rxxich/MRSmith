package server

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"mrsmith/internal/apperr"
	"mrsmith/internal/auth"
	"mrsmith/internal/rules"
)

type meResponse struct {
	User struct {
		Username           string    `json:"username"`
		PasswordChangedAt  time.Time `json:"passwordChangedAt"`
		MustChangePassword bool      `json:"mustChangePassword"`
	} `json:"user"`
	CSRF      string `json:"csrf"`
	PublicURL string `json:"publicUrl"`
	PanelPath string `json:"panelPath"`
	Version   string `json:"version"`
	Limits    struct {
		MaxUpload int64 `json:"maxUpload"`
		MaxFetch  int64 `json:"maxFetch"`
	} `json:"limits"`
	Formats []rules.OutputFormat `json:"formats"`
}

func (s *Server) me(sess *auth.Session, u *auth.User) meResponse {
	var m meResponse
	m.User.Username = u.Username
	m.User.PasswordChangedAt = u.PasswordChangedAt
	m.User.MustChangePassword = u.MustChangePassword
	m.CSRF = sess.CSRF
	m.PublicURL = s.cfg.PublicURL
	m.PanelPath = s.cfg.PanelPath
	m.Version = s.version
	m.Limits.MaxUpload = s.cfg.MaxUploadBytes
	m.Limits.MaxFetch = s.cfg.MaxFetchBytes
	m.Formats = rules.AllOutputs
	return m
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decodeJSON(w, r, &in, 8<<10); err != nil {
		return err
	}
	if s.cfg.CookieSecure && !s.isHTTPS(r) && !isLoopback(r.Host) {
		// The browser would silently drop the Secure session cookie.
		return apperr.Bad("панель открыта по HTTP, а вход работает только по HTTPS. Откройте её через reverse proxy с TLS " +
			"(он должен передавать X-Forwarded-Proto: https) или, только для теста, задайте MRSMITH_COOKIE_SECURE=false")
	}
	ip := s.clientIP(r)
	token, sess, user, err := s.auth.Login(r.Context(), in.Username, in.Password, ip, r.UserAgent())
	if err != nil {
		s.log.Warn("login failed", "ip", ip, "username", truncate(in.Username, 64))
		return err
	}
	s.log.Info("login", "ip", ip, "username", user.Username)
	s.setCookie(w, token)
	writeJSON(w, http.StatusOK, s.me(sess, user))
	return nil
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) error {
	if c, err := r.Cookie(s.cookieName()); err == nil {
		s.auth.Logout(c.Value)
	}
	s.clearCookie(w)
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) error {
	writeJSON(w, http.StatusOK, s.me(sessionFrom(r), userFrom(r)))
	return nil
}

func (s *Server) handlePassword(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Current string `json:"current"`
		Next    string `json:"next"`
	}
	if err := decodeJSON(w, r, &in, 8<<10); err != nil {
		return err
	}
	if err := s.auth.ChangePassword(r.Context(), userFrom(r), in.Current, in.Next, sessionFrom(r).ID); err != nil {
		return err
	}
	s.log.Info("password changed", "username", userFrom(r).Username, "ip", s.clientIP(r))
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (s *Server) handleUsername(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decodeJSON(w, r, &in, 8<<10); err != nil {
		return err
	}
	old := userFrom(r).Username
	if err := s.auth.ChangeUsername(r.Context(), userFrom(r), in.Username, in.Password); err != nil {
		return err
	}
	s.log.Info("username changed", "from", old, "to", in.Username, "ip", s.clientIP(r))
	w.WriteHeader(http.StatusNoContent)
	return nil
}

type sessionView struct {
	ID        string    `json:"id"`
	Current   bool      `json:"current"`
	CreatedAt time.Time `json:"createdAt"`
	LastSeen  time.Time `json:"lastSeen"`
	ExpiresAt time.Time `json:"expiresAt"`
	IP        string    `json:"ip"`
	UserAgent string    `json:"userAgent"`
}

func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) error {
	list, err := s.auth.Sessions(userFrom(r).ID)
	if err != nil {
		return err
	}
	cur := sessionFrom(r).ID
	out := make([]sessionView, 0, len(list))
	for _, x := range list {
		out = append(out, sessionView{x.ID, x.ID == cur, x.CreatedAt, x.LastSeen, x.ExpiresAt, x.IP, x.UserAgent})
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

func (s *Server) handleRevokeSession(w http.ResponseWriter, r *http.Request) error {
	if err := s.auth.Revoke(userFrom(r).ID, r.PathValue("id")); err != nil {
		return badRequest("сессия не найдена")
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// isLoopback reports whether the Host header names this machine; browsers
// accept Secure cookies there even over plain HTTP.
func isLoopback(host string) bool {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") || strings.HasSuffix(strings.ToLower(host), ".localhost") {
		return true
	}
	a, err := netip.ParseAddr(host)
	return err == nil && a.IsLoopback()
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
