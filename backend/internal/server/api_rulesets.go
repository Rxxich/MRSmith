package server

import (
	"context"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"mrsmith/internal/apperr"
	"mrsmith/internal/rules"
	"mrsmith/internal/rulesets"
	"mrsmith/internal/storage"
	"mrsmith/internal/store"
)

type rulesetView struct {
	*rulesets.Ruleset
	Running bool `json:"running"`
}

func (s *Server) view(rs *rulesets.Ruleset) rulesetView {
	return rulesetView{Ruleset: rs, Running: s.rs.IsRunning(rs.ID)}
}

func pathID(r *http.Request) (string, error) {
	id := r.PathValue("id")
	if !store.IsID(id) {
		return "", rulesets.ErrNotFound
	}
	return id, nil
}

func (s *Server) handleRulesets(w http.ResponseWriter, r *http.Request) error {
	s.flushDownloads()
	list, err := s.rs.List()
	if err != nil {
		return err
	}
	out := make([]rulesetView, 0, len(list))
	for i := range list {
		out = append(out, s.view(&list[i]))
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

func (s *Server) handleRuleset(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r)
	if err != nil {
		return err
	}
	s.flushDownloads()
	rs, err := s.rs.Get(id)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, s.view(rs))
	return nil
}

// buildInBackground starts a rebuild that outlives the request.
func (s *Server) buildInBackground(id string, force bool, trigger string) {
	go func() {
		ctx, cancel := context.WithTimeout(s.bg, 10*time.Minute)
		defer cancel()
		if _, _, err := s.rs.Refresh(ctx, id, force, trigger); err != nil {
			s.log.Warn("background build failed", "id", id, "err", err)
		}
	}()
}

func (s *Server) handleCreateRuleset(w http.ResponseWriter, r *http.Request) error {
	var in rulesets.Input
	if err := decodeJSON(w, r, &in, 8<<20); err != nil {
		return err
	}
	rs, err := s.rs.Create(in)
	if err != nil {
		return err
	}
	s.buildInBackground(rs.ID, true, "create")
	v := s.view(rs)
	v.Running = true
	writeJSON(w, http.StatusCreated, v)
	return nil
}

func (s *Server) handleUpdateRuleset(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r)
	if err != nil {
		return err
	}
	var in rulesets.Input
	if err := decodeJSON(w, r, &in, 8<<20); err != nil {
		return err
	}
	rs, err := s.rs.Update(id, in)
	if err != nil {
		return err
	}
	s.buildInBackground(rs.ID, false, "edit")
	v := s.view(rs)
	v.Running = true
	writeJSON(w, http.StatusOK, v)
	return nil
}

func (s *Server) handleDeleteRuleset(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r)
	if err != nil {
		return err
	}
	if err := s.rs.Delete(id); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (s *Server) handleRefreshRuleset(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r)
	if err != nil {
		return err
	}
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(6 * time.Minute))
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	rs, ev, err := s.rs.Refresh(ctx, id, true, "manual")
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"ruleset": s.view(rs), "event": ev})
	return nil
}

func (s *Server) handleEntries(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r)
	if err != nil {
		return err
	}
	q := r.URL.Query()
	offset, _ := strconv.Atoi(q.Get("offset"))
	limit, _ := strconv.Atoi(q.Get("limit"))
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	total, items, err := s.rs.Entries(id, truncate(q.Get("q"), 200), offset, limit)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"total": total, "offset": offset, "items": items})
	return nil
}

func (s *Server) handleEditEntries(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r)
	if err != nil {
		return err
	}
	var in struct {
		Add    []string          `json:"add"`
		Remove []rules.Exclusion `json:"remove"`
	}
	if err := decodeJSON(w, r, &in, 4<<20); err != nil {
		return err
	}
	if len(in.Add)+len(in.Remove) == 0 {
		return badRequest("нечего менять")
	}
	if len(in.Add)+len(in.Remove) > 10000 {
		return badRequest("не больше 10000 правил за раз")
	}
	rs, err := s.rs.EditEntries(r.Context(), id, in.Add, in.Remove)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, s.view(rs))
	return nil
}

func (s *Server) handlePreviewExclusion(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r)
	if err != nil {
		return err
	}
	var in rules.Exclusion
	if err := decodeJSON(w, r, &in, 8<<10); err != nil {
		return err
	}
	p, err := s.rs.PreviewExclusion(id, in)
	if err != nil {
		return err
	}
	const shown = 200
	resp := map[string]any{"removedCount": len(p.Removed), "addedCount": len(p.Added), "still": p.Still}
	resp["removed"] = p.Removed[:min(len(p.Removed), shown)]
	resp["added"] = p.Added[:min(len(p.Added), shown)]
	writeJSON(w, http.StatusOK, resp)
	return nil
}

func (s *Server) handleClearOverrides(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r)
	if err != nil {
		return err
	}
	var in struct {
		Include []string `json:"include"`
		Exclude []string `json:"exclude"`
	}
	if err := decodeJSON(w, r, &in, 4<<20); err != nil {
		return err
	}
	rs, err := s.rs.ClearOverrides(r.Context(), id, in.Include, in.Exclude)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, s.view(rs))
	return nil
}

func (s *Server) handleRotateToken(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r)
	if err != nil {
		return err
	}
	rs, err := s.rs.RotateToken(id)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, s.view(rs))
	return nil
}

func (s *Server) handleRulesetEvents(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r)
	if err != nil {
		return err
	}
	evs, err := s.rs.Events(id, 100)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, evs)
	return nil
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) error {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	evs, err := s.rs.Events("", limit)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, evs)
	return nil
}

func (s *Server) handleDownloadOutput(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r)
	if err != nil {
		return err
	}
	rs, err := s.rs.Get(id)
	if err != nil {
		return err
	}
	f := rules.OutputFormat(r.PathValue("format"))
	return s.sendOutput(w, r, rs, f, true)
}

func (s *Server) sendOutput(w http.ResponseWriter, r *http.Request, rs *rulesets.Ruleset, f rules.OutputFormat, attachment bool) error {
	info, ok := rs.State.Outputs[f]
	if !ok || info.Error != "" || !f.Valid() {
		return apperr.NotFound("файл ещё не собран")
	}
	file, err := os.Open(s.rs.OutputPath(rs.ID, f))
	if err != nil {
		return apperr.NotFound("файл ещё не собран")
	}
	defer file.Close()
	st, err := file.Stat()
	if err != nil {
		return err
	}
	h := w.Header()
	h.Set("Content-Type", f.ContentType())
	h.Set("ETag", `"`+info.SHA256+`"`)
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Rule-Count", strconv.Itoa(rs.State.Count))
	name := rs.Slug + "." + string(f)
	disp := "inline"
	if attachment {
		disp = "attachment"
	}
	h.Set("Content-Disposition", mime.FormatMediaType(disp, map[string]string{"filename": name}))
	http.ServeContent(w, r, name, st.ModTime(), file)
	return nil
}

func (s *Server) handleSourceUpload(w http.ResponseWriter, r *http.Request) error {
	if err := s.st.EnsureSystemBucket(storage.SourcesBucket); err != nil {
		return err
	}
	_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(time.Hour))
	r.Body = http.MaxBytesReader(w, r.Body, s.cfg.MaxFetchBytes+1<<20)
	mr, err := r.MultipartReader()
	if err != nil {
		return badRequest("ожидается multipart/form-data")
	}
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			return badRequest("файл не передан")
		}
		if err != nil {
			return err
		}
		if part.FormName() != "file" || part.FileName() == "" {
			part.Close()
			continue
		}
		name := safeFileName(part.FileName())
		key := store.RandomHex(4) + "-" + name
		obj, err := s.st.Put(storage.SourcesBucket, key, io.LimitReader(part, s.cfg.MaxFetchBytes+1), "")
		part.Close()
		if err != nil {
			return err
		}
		if obj.Size > s.cfg.MaxFetchBytes {
			_, _ = s.st.DeleteObjects(storage.SourcesBucket, []string{key}, nil)
			return apperr.TooLarge(fmt.Sprintf("файл больше %d МБ", s.cfg.MaxFetchBytes>>20))
		}
		writeJSON(w, http.StatusCreated, map[string]string{"bucket": obj.Bucket, "key": obj.Key})
		return nil
	}
}

// safeFileName keeps the base name of an uploaded file and drops anything a
// key could not hold.
func safeFileName(name string) string {
	name = path.Base(strings.ReplaceAll(name, "\\", "/"))
	var b strings.Builder
	for _, r := range name {
		if r < 0x20 || r == 0x7f || r == '/' || r == '\\' {
			continue
		}
		b.WriteRune(r)
	}
	out := strings.TrimSpace(b.String())
	if out == "" || out == "." || out == ".." {
		out = "file"
	}
	if len(out) > 200 {
		out = out[len(out)-200:]
	}
	return out
}

func (s *Server) handleOverview(w http.ResponseWriter, r *http.Request) error {
	list, err := s.rs.List()
	if err != nil {
		return err
	}
	type rsStats struct {
		Total   int `json:"total"`
		OK      int `json:"ok"`
		Errors  int `json:"errors"`
		Rules   int `json:"rules"`
		Sources int `json:"sources"`
		Auto    int `json:"auto"`
	}
	var st rsStats
	for _, x := range list {
		st.Total++
		st.Rules += x.State.Count
		st.Sources += len(x.Sources)
		if x.IntervalMin > 0 {
			st.Auto++
		}
		switch {
		case x.State.Status == rulesets.StatusError || x.State.Error != "":
			st.Errors++
		case x.State.Status == rulesets.StatusOK:
			st.OK++
		}
	}
	buckets, err := s.st.Buckets()
	if err != nil {
		return err
	}
	objects, bytes := s.st.TotalBytes()
	evs, err := s.rs.Events("", 12)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"rulesets": st,
		"storage":  map[string]any{"buckets": len(buckets), "objects": objects, "bytes": bytes},
		"events":   evs,
		"version":  s.version,
	})
	return nil
}
