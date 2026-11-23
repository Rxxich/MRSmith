// Package notify sends messages to the admins through a Telegram bot.
package notify

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"mrsmith/internal/netproxy"
)

const (
	apiBase  = "https://api.telegram.org"
	maxText  = 4000 // Telegram allows 4096 characters
	queueLen = 100
)

type Telegram struct {
	token string
	chats []int64
	proxy *netproxy.Proxy
	http  *http.Client
	// upload sends files: same connection, longer time limit.
	upload *http.Client
	base   string
	log    *slog.Logger
	queue  chan string
	gap    time.Duration
}

func NewTelegram(token string, chats []int64, p *netproxy.Proxy, log *slog.Logger) *Telegram {
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	dial := dialer.DialContext
	if p != nil {
		dial = func(ctx context.Context, network, addr string) (net.Conn, error) {
			return p.DialContext(ctx, network, addr, nil)
		}
	}
	transport := &http.Transport{
		Proxy:               nil,
		DialContext:         dial,
		ForceAttemptHTTP2:   true,
		TLSHandshakeTimeout: 10 * time.Second,
		IdleConnTimeout:     90 * time.Second,
		TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS12},
	}
	noRedirect := func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Telegram{
		token: token, chats: chats, proxy: p, base: apiBase, log: log,
		queue: make(chan string, queueLen), gap: 1100 * time.Millisecond,
		http:   &http.Client{Timeout: 30 * time.Second, Transport: transport, CheckRedirect: noRedirect},
		upload: &http.Client{Timeout: 15 * time.Minute, Transport: transport, CheckRedirect: noRedirect},
	}
}

// Chats returns how many recipients are configured.
func (t *Telegram) Chats() int { return len(t.chats) }

// Proxy returns the proxy used to reach Telegram, or nil.
func (t *Telegram) Proxy() *netproxy.Proxy { return t.proxy }

// Enqueue schedules an HTML message for every admin without blocking. When
// Telegram is unreachable for long, the oldest notices are dropped.
func (t *Telegram) Enqueue(html string) {
	select {
	case t.queue <- html:
	default:
		t.log.Warn("telegram queue is full, notice dropped")
	}
}

// Run delivers queued messages one at a time, keeping under Telegram's
// rate limits, until ctx ends.
func (t *Telegram) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case msg := <-t.queue:
			if err := t.Send(ctx, msg); err != nil && ctx.Err() == nil {
				t.log.Warn("telegram notice failed", "err", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(t.gap):
			}
		}
	}
}

// Send delivers one HTML message to every admin now.
func (t *Telegram) Send(ctx context.Context, html string) error {
	var errs []error
	for _, chat := range t.chats {
		if err := t.sendOne(ctx, chat, html); err != nil {
			errs = append(errs, fmt.Errorf("получатель %d: %w", chat, err))
		}
	}
	return errors.Join(errs...)
}

type apiReply struct {
	OK          bool   `json:"ok"`
	ErrorCode   int    `json:"error_code"`
	Description string `json:"description"`
	Parameters  struct {
		RetryAfter int `json:"retry_after"`
	} `json:"parameters"`
}

func (t *Telegram) sendOne(ctx context.Context, chat int64, html string) error {
	body, _ := json.Marshal(map[string]any{
		"chat_id":                  chat,
		"text":                     html,
		"parse_mode":               "HTML",
		"disable_web_page_preview": true,
	})
	for attempt := 0; ; attempt++ {
		reply, err := t.call(ctx, "sendMessage", body)
		if err != nil {
			return err
		}
		if reply.OK {
			return nil
		}
		if reply.ErrorCode == http.StatusTooManyRequests && attempt < 2 {
			wait := time.Duration(min(max(reply.Parameters.RetryAfter, 1), 60)) * time.Second
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(wait):
			}
			continue
		}
		return explain(reply)
	}
}

// MaxFile is the largest file a bot may send.
const MaxFile = 50 << 20

// SendFile delivers a file with an HTML caption to every admin now.
func (t *Telegram) SendFile(ctx context.Context, path, name, caption string) error {
	var errs []error
	for _, chat := range t.chats {
		if err := t.sendFileOne(ctx, chat, path, name, caption); err != nil {
			errs = append(errs, fmt.Errorf("получатель %d: %w", chat, err))
		}
	}
	return errors.Join(errs...)
}

func (t *Telegram) sendFileOne(ctx context.Context, chat int64, path, name, caption string) error {
	for attempt := 0; ; attempt++ {
		reply, err := t.postFile(ctx, chat, path, name, caption)
		if err != nil {
			return err
		}
		if reply.OK {
			return nil
		}
		if reply.ErrorCode == http.StatusTooManyRequests && attempt < 2 {
			wait := time.Duration(min(max(reply.Parameters.RetryAfter, 1), 60)) * time.Second
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(wait):
			}
			continue
		}
		return explain(reply)
	}
}

// postFile streams the file as multipart/form-data with a known length,
// so it never sits in memory whole.
func (t *Telegram) postFile(ctx context.Context, chat int64, path, name, caption string) (*apiReply, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() > MaxFile {
		return nil, fmt.Errorf("файл больше %d МБ — столько Telegram не принимает от ботов", MaxFile>>20)
	}
	var head bytes.Buffer
	mw := multipart.NewWriter(&head)
	_ = mw.WriteField("chat_id", strconv.FormatInt(chat, 10))
	if caption != "" {
		_ = mw.WriteField("caption", caption)
		_ = mw.WriteField("parse_mode", "HTML")
	}
	if _, err := mw.CreateFormFile("document", name); err != nil {
		return nil, err
	}
	tail := "\r\n--" + mw.Boundary() + "--\r\n"
	body := io.MultiReader(&head, f, strings.NewReader(tail))
	size := int64(head.Len()) + info.Size() + int64(len(tail))

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.base+"/bot"+t.token+"/sendDocument", body)
	if err != nil {
		return nil, errors.New("не удалось подготовить запрос к Telegram")
	}
	req.ContentLength = size
	req.Header.Set("Content-Type", mw.FormDataContentType())
	return t.do(t.upload, req)
}

func (t *Telegram) call(ctx context.Context, method string, body []byte) (*apiReply, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.base+"/bot"+t.token+"/"+method, bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("не удалось подготовить запрос к Telegram")
	}
	req.Header.Set("Content-Type", "application/json")
	return t.do(t.http, req)
}

func (t *Telegram) do(c *http.Client, req *http.Request) (*apiReply, error) {
	resp, err := c.Do(req)
	if err != nil {
		return nil, t.scrub(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return nil, t.scrub(err)
	}
	var reply apiReply
	if err := json.Unmarshal(raw, &reply); err != nil {
		return nil, fmt.Errorf("Telegram ответил %d без понятного текста", resp.StatusCode)
	}
	return &reply, nil
}

// scrub drops the request URL from transport errors: it carries the token.
func (t *Telegram) scrub(err error) error {
	var uerr *url.Error
	if errors.As(err, &uerr) {
		err = uerr.Err
	}
	msg := strings.ReplaceAll(err.Error(), t.token, "…")
	var perr *netproxy.Error
	switch {
	case errors.As(err, &perr):
		return fmt.Errorf("не удалось подключиться к Telegram через прокси %s: %s", perr.Proxy, strings.ReplaceAll(perr.Err.Error(), t.token, "…"))
	case errors.Is(err, context.DeadlineExceeded) || strings.Contains(msg, "Client.Timeout"):
		return errors.New("Telegram не ответил вовремя — если api.telegram.org недоступен с сервера, задайте TELEGRAM_PROXY")
	}
	return fmt.Errorf("нет связи с Telegram: %s", msg)
}

func explain(r *apiReply) error {
	d := r.Description
	switch {
	case r.ErrorCode == http.StatusUnauthorized:
		return errors.New("Telegram не принял TELEGRAM_TOKEN — проверьте токен у @BotFather")
	case strings.Contains(d, "chat not found"):
		return errors.New("чат не найден — откройте бота в Telegram и нажмите «Запустить» (/start), проверьте ADMIN_ID")
	case strings.Contains(d, "bot was blocked"):
		return errors.New("бот заблокирован этим пользователем — разблокируйте его в Telegram")
	case d != "":
		return fmt.Errorf("Telegram: %s", d)
	}
	return fmt.Errorf("Telegram ответил ошибкой %s", strconv.Itoa(r.ErrorCode))
}
