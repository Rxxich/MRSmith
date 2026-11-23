package server

import (
	"net/http"
	"net/url"
	"strings"

	"mrsmith/internal/apperr"
	"mrsmith/internal/downloads"
	"mrsmith/internal/rules"
	"mrsmith/internal/rulesets"
)

// serveRuleset answers /r/{slug}.{format}, the links mihomo clients fetch.
func (s *Server) serveRuleset(w http.ResponseWriter, r *http.Request) {
	if !safeMethod(r.Method) {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/r/")
	dot := strings.LastIndexByte(name, '.')
	if dot <= 0 || strings.Contains(name, "/") {
		http.NotFound(w, r)
		return
	}
	slug, format := name[:dot], rules.OutputFormat(name[dot+1:])
	rs, err := s.rs.BySlug(slug)
	if err != nil || !format.Valid() {
		http.NotFound(w, r)
		return
	}
	if !rulesets.CheckToken(rs, r.URL.Query().Get("token")) {
		// The same answer as for a missing set, so links cannot be probed.
		// The journal tells the admin what really happened.
		if res, ok := failure(r, downloads.OutcomeBadToken); ok && s.dl != nil {
			s.dl.Ruleset(rs.ID, rs.Slug, format, s.clientIP(r), r.UserAgent(), res)
		}
		http.NotFound(w, r)
		return
	}
	h := w.Header()
	h.Set("Cross-Origin-Resource-Policy", "cross-origin")
	if !rs.Private {
		h.Set("Access-Control-Allow-Origin", "*")
	}
	h.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	rw := &recordingWriter{ResponseWriter: w}
	if err := s.sendOutput(rw, r, rs, format, false); err != nil {
		if e, ok := apperr.As(err); ok && e.Status == http.StatusNotFound {
			http.NotFound(rw, r)
		} else {
			s.writeError(rw, r, err)
		}
	}
	if res, ok := result(r, rw); ok && s.dl != nil {
		s.dl.Ruleset(rs.ID, rs.Slug, format, s.clientIP(r), r.UserAgent(), res)
	}
}

// serveFile answers /f/{bucket}/{key} for public buckets.
func (s *Server) serveFile(w http.ResponseWriter, r *http.Request) {
	if !safeMethod(r.Method) {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	raw := strings.TrimPrefix(r.URL.EscapedPath(), "/f/")
	bucket, escKey, ok := strings.Cut(raw, "/")
	if !ok || bucket == "" || escKey == "" {
		http.NotFound(w, r)
		return
	}
	key, err := url.PathUnescape(escKey)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	b, err := s.st.Bucket(bucket)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	turnAway := func(outcome string) {
		if res, ok := failure(r, outcome); ok && s.dl != nil {
			s.dl.File(bucket, key, s.clientIP(r), r.UserAgent(), res)
		}
		http.NotFound(w, r)
	}
	if !b.Public {
		turnAway(downloads.OutcomePrivate)
		return
	}
	o, err := s.st.Object(bucket, key)
	if err != nil {
		turnAway(downloads.OutcomeMissing)
		return
	}
	h := w.Header()
	h.Set("Cross-Origin-Resource-Policy", "cross-origin")
	h.Set("Access-Control-Allow-Origin", "*")
	h.Set("Cache-Control", "public, max-age=300")
	rw := &recordingWriter{ResponseWriter: w}
	s.sendObject(rw, r, o, "public")
	if res, ok := result(r, rw); ok && s.dl != nil {
		s.dl.File(o.Bucket, o.Key, s.clientIP(r), r.UserAgent(), res)
	}
}
