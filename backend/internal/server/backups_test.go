package server

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mrsmith/internal/backup"
)

func TestBackupsAPI(t *testing.T) {
	e := newEnv(t, "/")
	if resp, _ := e.do("GET", "/api/backups", nil, nil); resp.StatusCode != 401 {
		t.Fatalf("backups without login: %d", resp.StatusCode)
	}
	e.login("/")
	resp, body := e.do("POST", "/api/rulesets", map[string]any{
		"name": "Twitch", "formats": []string{"yaml"}, "include": []string{"+.twitch.tv"},
	}, nil)
	if resp.StatusCode != 201 {
		t.Fatalf("create set: %d %s", resp.StatusCode, body)
	}

	resp, body = e.do("PUT", "/api/backups/settings", map[string]any{"everyHours": 24, "keep": 5, "telegram": true}, nil)
	if resp.StatusCode != 400 || !strings.Contains(string(body), "Telegram не настроен") {
		t.Fatalf("telegram without a bot: %d %s", resp.StatusCode, body)
	}
	resp, body = e.do("PUT", "/api/backups/settings", map[string]any{"everyHours": 24, "keep": 6, "linkBase": "https://rules.example.com"}, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("settings: %d %s", resp.StatusCode, body)
	}
	var st backup.Status
	json.Unmarshal(body, &st)
	if !st.Ready || st.Settings.EveryHours != 24 || st.Settings.Keep != 6 || st.NextAutoAt.IsZero() {
		t.Fatalf("status: %+v", st)
	}

	resp, body = e.do("POST", "/api/backups", map[string]any{"linkBase": "https://rules.example.com"}, nil)
	if resp.StatusCode != 202 {
		t.Fatalf("create: %d %s", resp.StatusCode, body)
	}
	e.bk.Wait()
	_, body = e.do("GET", "/api/backups", nil, nil)
	json.Unmarshal(body, &st)
	if len(st.Files) != 1 || st.Files[0].Manifest == nil || st.Files[0].Manifest.LinkBase != "https://rules.example.com" {
		t.Fatalf("files: %s", body)
	}
	name := st.Files[0].Name

	resp, body = e.do("GET", "/api/backups/files/"+url.PathEscape(name), nil, nil)
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "application/zip" ||
		!strings.Contains(resp.Header.Get("Content-Disposition"), "attachment") || !strings.HasPrefix(string(body), "PK") {
		t.Fatalf("download: %d %v", resp.StatusCode, resp.Header)
	}
	archive := body

	// Uploads land in the folder under their own name; junk is refused.
	buf, ct := multipartBody(nil, "file", "from-old-server.zip", archive)
	resp, body = e.do("POST", "/api/backups/upload", buf, map[string]string{"Content-Type": ct})
	if resp.StatusCode != 200 || !strings.Contains(string(body), `"name":"from-old-server.zip"`) {
		t.Fatalf("upload: %d %s", resp.StatusCode, body)
	}
	buf, ct = multipartBody(nil, "file", "notes.zip", []byte("hello"))
	if resp, body = e.do("POST", "/api/backups/upload", buf, map[string]string{"Content-Type": ct}); resp.StatusCode != 400 {
		t.Fatalf("junk upload: %d %s", resp.StatusCode, body)
	}
	if _, err := os.Stat(filepath.Join(e.bkDir, "notes.zip")); err == nil {
		t.Fatal("junk upload kept")
	}

	for _, bad := range []string{"..%2Fmrsmith.db", "%2E%2E%2Fx.zip", "x.txt"} {
		if resp, _ := e.do("GET", "/api/backups/files/"+bad, nil, nil); resp.StatusCode != 400 && resp.StatusCode != 404 {
			t.Fatalf("download %s: %d", bad, resp.StatusCode)
		}
		if resp, _ := e.do("DELETE", "/api/backups/files/"+bad, nil, nil); resp.StatusCode != 400 && resp.StatusCode != 404 {
			t.Fatalf("delete %s: %d", bad, resp.StatusCode)
		}
	}

	resp, body = e.do("POST", "/api/backups/restore", map[string]any{"name": "from-old-server.zip", "linkBase": e.srv.URL}, nil)
	if resp.StatusCode != 202 {
		t.Fatalf("restore: %d %s", resp.StatusCode, body)
	}
	e.bk.Wait()
	_, body = e.do("GET", "/api/backups", nil, nil)
	json.Unmarshal(body, &st)
	if st.Last == nil || st.Last.Op != "restore" || st.Last.Error != "" || st.Last.Restore == nil || st.Last.Restore.Rulesets != 1 {
		t.Fatalf("restore result: %s", body)
	}
	if st.Last.Restore.Safety == "" {
		t.Fatal("no safety copy before restore")
	}

	if resp, _ := e.do("DELETE", "/api/backups/files/"+url.PathEscape(name), nil, nil); resp.StatusCode != 204 {
		t.Fatalf("delete: %d", resp.StatusCode)
	}
	if _, err := os.Stat(filepath.Join(e.bkDir, name)); err == nil {
		t.Fatal("file not deleted")
	}
	if resp, _ := e.do("POST", "/api/backups/restore", map[string]any{"name": name}, nil); resp.StatusCode != 404 {
		t.Fatalf("restore of a missing file: %d", resp.StatusCode)
	}
}
