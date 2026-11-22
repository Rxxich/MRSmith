package server

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"mrsmith/internal/apperr"
	"mrsmith/internal/auth"
)

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(true)
	_ = enc.Encode(v)
}

type errorBody struct {
	Error string `json:"error"`
}

// writeError maps errors to HTTP statuses. Unknown errors are logged and
// replaced with a generic message so internals never leak.
func (s *Server) writeError(w http.ResponseWriter, r *http.Request, err error) {
	var blocked *auth.BlockedError
	if errors.As(err, &blocked) {
		w.Header().Set("Retry-After", strconv.Itoa(int(blocked.Wait.Seconds())+1))
		writeJSON(w, http.StatusTooManyRequests, errorBody{blocked.Error()})
		return
	}
	if e, ok := apperr.As(err); ok {
		writeJSON(w, e.Status, errorBody{e.Msg})
		return
	}
	var mbe *http.MaxBytesError
	if errors.As(err, &mbe) {
		writeJSON(w, http.StatusRequestEntityTooLarge, errorBody{"запрос слишком большой"})
		return
	}
	s.log.Error("request failed", "method", r.Method, "path", r.URL.Path, "err", err)
	writeJSON(w, http.StatusInternalServerError, errorBody{"внутренняя ошибка сервера, подробности в логах"})
}

func badRequest(msg string) error { return apperr.New(http.StatusBadRequest, "%s", msg) }

// decodeJSON reads a JSON body of at most limit bytes into v.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any, limit int64) error {
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		return apperr.New(http.StatusUnsupportedMediaType, "ожидается JSON")
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	if err := dec.Decode(v); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return err
		}
		return badRequest("некорректный JSON в запросе")
	}
	if _, err := dec.Token(); err != io.EOF {
		return badRequest("лишние данные после JSON")
	}
	return nil
}

// clientIP returns the address of the real client. Forwarded headers are
// trusted only when the direct peer is a configured reverse proxy.
func (s *Server) clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer, err := netip.ParseAddr(host)
	if err != nil {
		return host
	}
	peer = peer.Unmap()
	if !s.trusted(peer) {
		return peer.String()
	}
	// Walk X-Forwarded-For from the right, skipping our own proxies.
	if xff := r.Header.Values("X-Forwarded-For"); len(xff) > 0 {
		parts := strings.Split(strings.Join(xff, ","), ",")
		for i := len(parts) - 1; i >= 0; i-- {
			a, err := netip.ParseAddr(strings.TrimSpace(parts[i]))
			if err != nil {
				break
			}
			a = a.Unmap()
			if !s.trusted(a) || i == 0 {
				return a.String()
			}
		}
	}
	if xr := strings.TrimSpace(r.Header.Get("X-Real-IP")); xr != "" {
		if a, err := netip.ParseAddr(xr); err == nil {
			return a.Unmap().String()
		}
	}
	return peer.String()
}

func (s *Server) trusted(a netip.Addr) bool {
	for _, p := range s.cfg.TrustedProxies {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// isHTTPS reports whether the client connection is TLS, directly or via a trusted proxy.
func (s *Server) isHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	if a, err := netip.ParseAddr(host); err == nil && s.trusted(a.Unmap()) {
		return strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
	}
	return false
}

// ---- middleware ------------------------------------------------------------

type statusWriter struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += int64(n)
	return n, err
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (s *Server) withBase(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w}
		defer func() {
			if rec := recover(); rec != nil {
				s.log.Error("panic", "path", r.URL.Path, "panic", rec)
				if sw.status == 0 {
					writeJSON(sw, http.StatusInternalServerError, errorBody{"внутренняя ошибка сервера"})
				}
			}
			level := slog.LevelDebug
			if sw.status >= 500 {
				level = slog.LevelWarn
			}
			s.log.Log(r.Context(), level, "http", "method", r.Method, "path", r.URL.Path, "status", sw.status,
				"bytes", sw.bytes, "ms", time.Since(start).Milliseconds(), "ip", s.clientIP(r))
		}()
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Robots-Tag", "noindex, nofollow")
		if s.isHTTPS(r) {
			h.Set("Strict-Transport-Security", "max-age=31536000")
		}
		rc := http.NewResponseController(sw)
		_ = rc.SetReadDeadline(time.Now().Add(60 * time.Second))
		_ = rc.SetWriteDeadline(time.Now().Add(10 * time.Minute))
		next.ServeHTTP(sw, r)
	})
}

func setPanelHeaders(h http.Header) {
	h.Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data: blob:; "+
		"font-src 'self'; connect-src 'self'; media-src 'self' blob:; object-src 'none'; base-uri 'self'; form-action 'self'; frame-ancestors 'none'")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Cross-Origin-Opener-Policy", "same-origin")
	h.Set("Cross-Origin-Resource-Policy", "same-origin")
	h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=(), interest-cohort=()")
}

func setAPIHeaders(h http.Header) {
	h.Set("Cache-Control", "no-store")
	h.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Cross-Origin-Resource-Policy", "same-origin")
}
