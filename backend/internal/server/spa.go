package server

import (
	"bytes"
	"errors"
	"io/fs"
	"net/http"
	"path"
	"regexp"
	"strings"
	"time"
)

// spa serves the embedded single-page app. Unknown paths get index.html so
// client-side routes work after a reload.
type spa struct {
	files fs.FS
	index []byte
	panel string
	built time.Time
}

var baseTagRe = regexp.MustCompile(`<base href="[^"]*"\s*/?>`)

func newSPA(files fs.FS, panel string) (*spa, error) {
	index, err := fs.ReadFile(files, "index.html")
	if err != nil {
		return nil, errors.New("в сборке нет index.html — сначала соберите фронтенд")
	}
	if !baseTagRe.Match(index) {
		return nil, errors.New(`index.html должен содержать <base href="/">`)
	}
	index = baseTagRe.ReplaceAll(index, []byte(`<base href="`+panel+`">`))
	return &spa{files: files, index: index, panel: panel, built: time.Now()}, nil
}

func (s *spa) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !safeMethod(r.Method) {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	rel := strings.TrimPrefix(r.URL.Path, s.panel)
	rel = strings.TrimPrefix(path.Clean("/"+rel), "/")
	if rel != "" && rel != "index.html" {
		if f, err := s.files.Open(rel); err == nil {
			st, err := f.Stat()
			if err == nil && !st.IsDir() {
				if strings.HasPrefix(rel, "assets/") {
					// Vite puts a content hash in every asset name.
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				} else {
					w.Header().Set("Cache-Control", "no-cache")
				}
				http.ServeFileFS(w, r, s.files, rel)
				f.Close()
				return
			}
			f.Close()
		}
		if strings.HasPrefix(rel, "assets/") || path.Ext(rel) != "" {
			http.NotFound(w, r)
			return
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeContent(w, r, "index.html", s.built, bytes.NewReader(s.index))
}
