package notify

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

const token = "123456789:AAbbccddeeffgghhiijjkkllmmnnooppqq"

// fakeAPI answers sendMessage with handle; the returned func counts calls.
func fakeAPI(t *testing.T, handle func(chat int64, text string) (int, string)) (*Telegram, func() int) {
	var mu sync.Mutex
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/bot"+token+"/sendMessage" {
			http.NotFound(w, r)
			return
		}
		var in struct {
			ChatID int64  `json:"chat_id"`
			Text   string `json:"text"`
			Mode   string `json:"parse_mode"`
		}
		json.NewDecoder(r.Body).Decode(&in)
		if in.Mode != "HTML" {
			t.Errorf("parse_mode %q", in.Mode)
		}
		mu.Lock()
		got = append(got, in.Text)
		code, body := handle(in.ChatID, in.Text)
		mu.Unlock()
		w.WriteHeader(code)
		io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	tg := NewTelegram(token, []int64{42}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	tg.base = srv.URL
	tg.gap = time.Millisecond
	return tg, func() int {
		mu.Lock()
		defer mu.Unlock()
		return len(got)
	}
}

func TestSendAndRetry(t *testing.T) {
	calls := 0
	tg, got := fakeAPI(t, func(int64, string) (int, string) {
		calls++
		if calls == 1 {
			return 429, `{"ok":false,"error_code":429,"description":"Too Many Requests","parameters":{"retry_after":1}}`
		}
		return 200, `{"ok":true}`
	})
	if err := tg.Send(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}
	if n := got(); n != 2 {
		t.Fatalf("%d calls, want a retry after 429", n)
	}
}

func TestErrorsHideToken(t *testing.T) {
	tg, _ := fakeAPI(t, func(int64, string) (int, string) {
		return 400, `{"ok":false,"error_code":400,"description":"Bad Request: chat not found"}`
	})
	err := tg.Send(context.Background(), "x")
	if err == nil || !strings.Contains(err.Error(), "/start") {
		t.Fatalf("got %v", err)
	}
	tg.base = "http://127.0.0.1:1"
	err = tg.Send(context.Background(), "x")
	if err == nil || strings.Contains(err.Error(), token) || strings.Contains(err.Error(), "AAbb") {
		t.Fatalf("token leaked or no error: %v", err)
	}
}

func TestChangeMessage(t *testing.T) {
	msg := ChangeMessage(Change{
		Name: "Триггер <test>", Files: []string{"trigger.mrs"}, Trigger: "schedule",
		Added: 12, Removed: 1, Total: 61674,
		SampleAdded:   []string{"DOMAIN-SUFFIX,a&b.example", "1.2.3.0/24"},
		SampleRemoved: []string{"old.example"},
	})
	for _, want := range []string{"<b>Триггер &lt;test&gt;</b>", "+12 и −1", "61\u202f674 правила", "<code>trigger.mrs</code>",
		"по расписанию", "a&amp;b.example", "и ещё 10", "Убраны:\n<code>old.example</code>"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message lacks %q:\n%s", want, msg)
		}
	}
	long := make([]string, 30)
	for i := range long {
		long[i] = strings.Repeat("x", 600)
	}
	if n := len([]rune(ChangeMessage(Change{Name: "n", Added: 30, SampleAdded: long}))); n > maxText {
		t.Fatalf("message is %d characters", n)
	}
	if plural(1, "a", "b", "c") != "a" || plural(3, "a", "b", "c") != "b" || plural(11, "a", "b", "c") != "c" || plural(21, "a", "b", "c") != "a" {
		t.Fatal("plural")
	}
}

func TestQueueDelivers(t *testing.T) {
	tg, got := fakeAPI(t, func(int64, string) (int, string) { return 200, `{"ok":true}` })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go tg.Run(ctx)
	tg.Enqueue("one")
	tg.Enqueue("two")
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if got() == 2 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("delivered %d of 2", got())
}

func TestSendFileStreamsMultipart(t *testing.T) {
	var got struct {
		chat, caption, mode, name string
		body                      []byte
		length                    int64
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/bot"+token+"/sendDocument" {
			http.NotFound(w, r)
			return
		}
		got.length = r.ContentLength
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Error(err)
		}
		got.chat, got.caption, got.mode = r.FormValue("chat_id"), r.FormValue("caption"), r.FormValue("parse_mode")
		f, h, err := r.FormFile("document")
		if err != nil {
			t.Error(err)
			return
		}
		got.name = h.Filename
		got.body, _ = io.ReadAll(f)
		io.WriteString(w, `{"ok":true}`)
	}))
	defer srv.Close()
	tg := NewTelegram(token, []int64{42}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	tg.base = srv.URL
	path := t.TempDir() + "/b.zip"
	os.WriteFile(path, []byte("PK archive"), 0o600)
	if err := tg.SendFile(context.Background(), path, "mrsmith-1.zip", "<b>Бэкап</b>"); err != nil {
		t.Fatal(err)
	}
	if got.chat != "42" || got.caption != "<b>Бэкап</b>" || got.mode != "HTML" || got.name != "mrsmith-1.zip" || string(got.body) != "PK archive" || got.length <= 0 {
		t.Fatalf("got %+v", got)
	}
}
