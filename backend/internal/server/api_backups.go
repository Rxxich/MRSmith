package server

import (
	"io"
	"mime"
	"net/http"
	"time"

	"mrsmith/internal/apperr"
	"mrsmith/internal/backup"
)

var errNoBackups = apperr.NotFound("бэкапы в этой сборке недоступны")

func (s *Server) handleBackups(w http.ResponseWriter, r *http.Request) error {
	if s.bk == nil {
		return errNoBackups
	}
	writeJSON(w, http.StatusOK, s.bk.Status())
	return nil
}

func (s *Server) handleBackupSettings(w http.ResponseWriter, r *http.Request) error {
	if s.bk == nil {
		return errNoBackups
	}
	var in struct {
		backup.Settings
		LinkBase string `json:"linkBase"`
	}
	if err := decodeJSON(w, r, &in, 4<<10); err != nil {
		return err
	}
	if err := s.bk.SetSettings(in.Settings, in.LinkBase); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, s.bk.Status())
	return nil
}

func (s *Server) handleCreateBackup(w http.ResponseWriter, r *http.Request) error {
	if s.bk == nil {
		return errNoBackups
	}
	var in struct {
		LinkBase string `json:"linkBase"`
	}
	if err := decodeJSON(w, r, &in, 4<<10); err != nil {
		return err
	}
	if err := s.bk.Create(in.LinkBase); err != nil {
		return err
	}
	writeJSON(w, http.StatusAccepted, s.bk.Status())
	return nil
}

func (s *Server) handleRestoreBackup(w http.ResponseWriter, r *http.Request) error {
	if s.bk == nil {
		return errNoBackups
	}
	var in struct {
		Name     string `json:"name"`
		LinkBase string `json:"linkBase"`
	}
	if err := decodeJSON(w, r, &in, 4<<10); err != nil {
		return err
	}
	if err := s.bk.Restore(in.Name, in.LinkBase); err != nil {
		return err
	}
	writeJSON(w, http.StatusAccepted, s.bk.Status())
	return nil
}

func (s *Server) handleUploadBackup(w http.ResponseWriter, r *http.Request) error {
	if s.bk == nil {
		return errNoBackups
	}
	_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(time.Hour))
	r.Body = http.MaxBytesReader(w, r.Body, s.cfg.MaxUploadBytes+1<<20)
	mr, err := r.MultipartReader()
	if err != nil {
		return badRequest("ожидается multipart/form-data")
	}
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			return badRequest("в запросе нет файла")
		}
		if err != nil {
			return err
		}
		if part.FileName() == "" {
			part.Close()
			continue
		}
		f, err := s.bk.Save(part.FileName(), part, s.cfg.MaxUploadBytes)
		part.Close()
		if err != nil {
			return err
		}
		writeJSON(w, http.StatusOK, f)
		return nil
	}
}

func (s *Server) handleDownloadBackup(w http.ResponseWriter, r *http.Request) error {
	if s.bk == nil {
		return errNoBackups
	}
	name := r.PathValue("name")
	f, err := s.bk.Open(name)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	h := w.Header()
	h.Set("Content-Type", "application/zip")
	h.Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	http.ServeContent(w, r, name, info.ModTime(), f)
	return nil
}

func (s *Server) handleDeleteBackup(w http.ResponseWriter, r *http.Request) error {
	if s.bk == nil {
		return errNoBackups
	}
	if err := s.bk.Delete(r.PathValue("name")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}
