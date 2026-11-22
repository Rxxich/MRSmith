package server

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"mrsmith/internal/audit"
)

func (s *Server) handleAuditCandidates(w http.ResponseWriter, r *http.Request) error {
	list, err := s.audit.Candidates()
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, list)
	return nil
}

func (s *Server) handleAuditLookup(w http.ResponseWriter, r *http.Request) error {
	res, err := s.audit.Lookup(r.URL.Query().Get("q"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, res)
	return nil
}

func (s *Server) handleAuditRun(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Providers []string `json:"providers"`
	}
	if err := decodeJSON(w, r, &in, 1<<20); err != nil {
		return err
	}
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(6 * time.Minute))
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	rep, err := s.audit.Run(ctx, in.Providers)
	if err != nil {
		return err
	}
	s.log.Info("audit", "providers", rep.Totals.Providers, "rules", rep.Totals.Rules, "findings", rep.Totals.Findings, "ms", rep.DurationMs)
	writeJSON(w, http.StatusOK, rep)
	return nil
}

func (s *Server) handleAuditReport(w http.ResponseWriter, r *http.Request) error {
	rep, err := s.audit.Report(r.PathValue("id"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, rep)
	return nil
}

func (s *Server) handleAuditFindings(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()
	query := audit.Query{Kind: q.Get("kind"), Provider: -1, Text: truncate(q.Get("q"), 200), Limit: 50}
	if v, err := strconv.Atoi(q.Get("provider")); err == nil {
		query.Provider = v
	}
	if v, err := strconv.Atoi(q.Get("offset")); err == nil && v > 0 {
		query.Offset = v
	}
	if v, err := strconv.Atoi(q.Get("limit")); err == nil && v > 0 && v <= 200 {
		query.Limit = v
	}
	total, items, err := s.audit.Findings(r.PathValue("id"), query)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"total": total, "offset": query.Offset, "items": items})
	return nil
}

func (s *Server) handleAuditFix(w http.ResponseWriter, r *http.Request) error {
	var in audit.FixRequest
	if err := decodeJSON(w, r, &in, 4<<20); err != nil {
		return err
	}
	if len(in.Findings) > 50_000 {
		return badRequest("слишком много находок за раз")
	}
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(11 * time.Minute))
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	results, err := s.audit.Fix(ctx, r.PathValue("id"), in)
	if err != nil {
		return err
	}
	s.log.Info("audit fix applied", "providers", len(results))
	writeJSON(w, http.StatusOK, results)
	return nil
}
