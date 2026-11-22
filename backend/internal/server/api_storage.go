package server

import (
	"io"
	"mime"
	"net/http"
	"sort"
	"strings"
	"time"

	"mrsmith/internal/storage"
)

type bucketView struct {
	storage.Bucket
	storage.BucketStats
}

func (s *Server) handleBuckets(w http.ResponseWriter, r *http.Request) error {
	list, err := s.st.Buckets()
	if err != nil {
		return err
	}
	out := make([]bucketView, 0, len(list))
	for _, b := range list {
		out = append(out, bucketView{b, s.st.Stats(b.Name)})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].System != out[j].System {
			return !out[i].System
		}
		return out[i].Name < out[j].Name
	})
	writeJSON(w, http.StatusOK, out)
	return nil
}

func (s *Server) handleCreateBucket(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Name   string `json:"name"`
		Public bool   `json:"public"`
	}
	if err := decodeJSON(w, r, &in, 4<<10); err != nil {
		return err
	}
	b, err := s.st.CreateBucket(strings.TrimSpace(in.Name), in.Public)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, bucketView{Bucket: *b})
	return nil
}

func (s *Server) handleUpdateBucket(w http.ResponseWriter, r *http.Request) error {
	if err := s.st.Writable(r.PathValue("name")); err != nil {
		return err
	}
	var in struct {
		Public bool `json:"public"`
	}
	if err := decodeJSON(w, r, &in, 4<<10); err != nil {
		return err
	}
	b, err := s.st.SetBucketPublic(r.PathValue("name"), in.Public)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, bucketView{*b, s.st.Stats(b.Name)})
	return nil
}

func (s *Server) handleDeleteBucket(w http.ResponseWriter, r *http.Request) error {
	if err := s.st.DeleteBucket(r.PathValue("name"), r.URL.Query().Get("force") == "1"); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (s *Server) handleListObjects(w http.ResponseWriter, r *http.Request) error {
	s.flushDownloads()
	l, err := s.st.List(r.PathValue("name"), r.URL.Query().Get("prefix"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, l)
	return nil
}

// handleUpload streams multipart files straight to disk. The "prefix" field,
// when sent before the files, places them in a folder; a "path" field sent
// right before a file overrides its name (used for dropped folders).
func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) error {
	bucket := r.PathValue("name")
	if err := s.st.Writable(bucket); err != nil {
		return err
	}
	_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(time.Hour))
	r.Body = http.MaxBytesReader(w, r.Body, s.cfg.MaxUploadBytes+1<<20)
	mr, err := r.MultipartReader()
	if err != nil {
		return badRequest("ожидается multipart/form-data")
	}
	prefix, nextPath := "", ""
	var saved []storage.Object
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		switch {
		case part.FileName() == "" && (part.FormName() == "prefix" || part.FormName() == "path"):
			v, _ := io.ReadAll(io.LimitReader(part, 1024))
			if part.FormName() == "prefix" {
				if prefix, err = storage.CleanPrefix(strings.TrimSpace(string(v))); err != nil {
					part.Close()
					return err
				}
			} else {
				nextPath = strings.TrimSpace(string(v))
			}
		case part.FileName() != "":
			name := safeFileName(part.FileName())
			if nextPath != "" {
				name = nextPath
				nextPath = ""
			}
			obj, err := s.st.Put(bucket, prefix+name, part, "")
			if err != nil {
				part.Close()
				return err
			}
			saved = append(saved, *obj)
		}
		part.Close()
	}
	if len(saved) == 0 {
		return badRequest("файлы не переданы")
	}
	writeJSON(w, http.StatusCreated, saved)
	return nil
}

// handleGetObject streams an object to an authenticated user (previews and
// downloads of private buckets).
func (s *Server) handleGetObject(w http.ResponseWriter, r *http.Request) error {
	o, err := s.st.Object(r.PathValue("name"), r.URL.Query().Get("key"))
	if err != nil {
		return err
	}
	disp := "inline"
	if r.URL.Query().Get("download") == "1" {
		disp = "attachment"
	}
	s.sendObject(w, r, o, disp)
	return nil
}

func (s *Server) sendObject(w http.ResponseWriter, r *http.Request, o *storage.Object, disposition string) {
	f, err := s.st.Open(o)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	h := w.Header()
	h.Set("Content-Type", o.ContentType)
	h.Set("ETag", `"`+o.SHA256+`"`)
	// User content is shown in a sandbox: even an uploaded HTML or SVG file
	// gets an opaque origin and cannot talk to the panel.
	h.Set("Content-Security-Policy", "sandbox allow-scripts allow-popups allow-forms; default-src * data: blob: 'unsafe-inline'; frame-ancestors 'self'")
	if disposition == "attachment" || strings.HasPrefix(o.ContentType, "text/html") && disposition != "public" {
		disposition = "attachment"
	} else {
		disposition = "inline"
	}
	h.Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": o.Name()}))
	http.ServeContent(w, r, o.Name(), o.UpdatedAt, f)
}

func (s *Server) handleDeleteObjects(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Keys     []string `json:"keys"`
		Prefixes []string `json:"prefixes"`
	}
	if err := decodeJSON(w, r, &in, 1<<20); err != nil {
		return err
	}
	for _, p := range in.Prefixes {
		if strings.TrimSpace(p) == "" {
			return badRequest("пустая папка — чтобы очистить бакет, удалите его")
		}
	}
	bucket := r.PathValue("name")
	if err := s.st.Writable(bucket); err != nil {
		return err
	}
	n, err := s.st.DeleteObjects(bucket, in.Keys, in.Prefixes)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]int{"deleted": n})
	return nil
}

func (s *Server) handleRenameObject(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		From string `json:"from"`
		To   string `json:"to"`
	}
	if err := decodeJSON(w, r, &in, 4<<10); err != nil {
		return err
	}
	if err := s.st.Writable(r.PathValue("name")); err != nil {
		return err
	}
	s.flushDownloads() // pending counts land on the old name, then move with it
	o, err := s.st.Rename(r.PathValue("name"), in.From, in.To)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, o)
	return nil
}
