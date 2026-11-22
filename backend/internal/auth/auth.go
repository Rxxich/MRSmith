package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"mrsmith/internal/apperr"
	"mrsmith/internal/store"
)

type User struct {
	ID                string    `json:"id"`
	Username          string    `json:"username"`
	PassHash          string    `json:"passHash"`
	CreatedAt         time.Time `json:"createdAt"`
	PasswordChangedAt time.Time `json:"passwordChangedAt"`
	// MustChangePassword is set while the password is still the generated one.
	MustChangePassword bool `json:"mustChangePassword"`
}

type Session struct {
	ID        string    `json:"id"`
	TokenHash string    `json:"tokenHash"`
	UserID    string    `json:"userId"`
	CSRF      string    `json:"csrf"`
	CreatedAt time.Time `json:"createdAt"`
	LastSeen  time.Time `json:"lastSeen"`
	ExpiresAt time.Time `json:"expiresAt"`
	IP        string    `json:"ip"`
	UserAgent string    `json:"userAgent"`
}

var (
	ErrInvalidCredentials = apperr.New(http.StatusUnauthorized, "неверный логин или пароль")
	ErrNoSession          = apperr.New(http.StatusUnauthorized, "нужно войти")
	ErrWrongPassword      = apperr.New(http.StatusBadRequest, "текущий пароль указан неверно")
)

// BlockedError tells the client how long to wait before the next attempt.
type BlockedError struct{ Wait time.Duration }

func (e *BlockedError) Error() string {
	return fmt.Sprintf("слишком много неудачных попыток, повторите через %s", e.Wait.Round(time.Second))
}

var usernameRe = regexp.MustCompile(`^[a-zA-Z0-9._-]{3,64}$`)

type Service struct {
	db        *store.DB
	ttl, idle time.Duration
	byIP      *limiter
	byUser    *limiter
	hashSem   chan struct{}
	dummyHash string
}

func New(db *store.DB, ttl, idle time.Duration) *Service {
	dummy, _ := HashPassword(GeneratePassword())
	return &Service{
		db:   db,
		ttl:  ttl,
		idle: idle,
		// 5 failures per IP in 15 min -> 5 min block, doubling up to 24 h.
		byIP: newLimiter(5, 15*time.Minute, 5*time.Minute, 24*time.Hour),
		// 20 failures per username from anywhere -> 5 min block, up to 1 h.
		// Kept short so an attacker cannot lock the owner out for long.
		byUser:    newLimiter(20, 15*time.Minute, 5*time.Minute, time.Hour),
		hashSem:   make(chan struct{}, 2),
		dummyHash: dummy,
	}
}

func userKey(name string) string { return strings.ToLower(strings.TrimSpace(name)) }

// Bootstrap creates the first administrator with a generated login and
// password when no user exists yet, and prints them once to out. Nothing
// secret ever has to live in configuration files.
func (s *Service) Bootstrap(out io.Writer) (username, password string, err error) {
	if s.db.Count(store.Users) > 0 {
		return "", "", nil
	}
	username = "smith-" + randomName(6)
	password = GeneratePassword()
	if err := s.createUser(username, password, true); err != nil {
		return "", "", err
	}
	printCredentials(out, "создан администратор", username, password)
	return username, password, nil
}

// printCredentials shows a login and password in a block that is easy to
// spot in container logs.
func printCredentials(out io.Writer, title, username, password string) {
	line := strings.Repeat("=", 64)
	fmt.Fprintf(out, "\n%s\n MRSmith: %s\n\n   логин:  %s\n   пароль: %s\n\n"+
		" Войдите и смените пароль в настройках. Эти данные больше не будут показаны.\n%s\n\n",
		line, title, username, password, line)
}

func randomName(n int) string {
	const alphabet = "abcdefghijkmnpqrstuvwxyz23456789"
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(b)
}

func (s *Service) createUser(username, password string, mustChange bool) error {
	hash, err := HashPassword(password)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	u := &User{ID: store.NewID(), Username: username, PassHash: hash, CreatedAt: now, PasswordChangedAt: now, MustChangePassword: mustChange}
	ok, err := s.db.Insert(store.Users, userKey(username), u)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("пользователь %s уже существует", username)
	}
	return nil
}

func (s *Service) verify(ctx context.Context, hash, password string) (bool, error) {
	select {
	case s.hashSem <- struct{}{}:
	case <-ctx.Done():
		return false, ctx.Err()
	}
	defer func() { <-s.hashSem }()
	return VerifyPassword(hash, password)
}

// Login checks credentials and opens a session. The returned token is only
// ever stored hashed.
func (s *Service) Login(ctx context.Context, username, password, ip, ua string) (string, *Session, *User, error) {
	displayIP := ip
	now := time.Now()
	key := userKey(username)
	ip = ipKey(ip)
	if wait := s.byIP.blocked(ip, now); wait > 0 {
		return "", nil, nil, &BlockedError{wait}
	}
	if wait := s.byUser.blocked(key, now); wait > 0 {
		return "", nil, nil, &BlockedError{wait}
	}
	if len(password) > MaxPasswordLen*4 || len(username) > 128 {
		s.byIP.fail(ip, now)
		return "", nil, nil, ErrInvalidCredentials
	}
	var u User
	err := s.db.Get(store.Users, key, &u)
	hash := u.PassHash
	if err != nil {
		hash = s.dummyHash // equalise timing for unknown users
	}
	ok, verr := s.verify(ctx, hash, password)
	if err != nil || verr != nil || !ok {
		s.byIP.fail(ip, now)
		s.byUser.fail(key, now)
		return "", nil, nil, ErrInvalidCredentials
	}
	s.byIP.success(ip)
	s.byUser.success(key)

	token := randomToken()
	sess := &Session{
		ID:        store.NewID(),
		TokenHash: hashToken(token),
		UserID:    u.ID,
		CSRF:      store.RandomHex(32),
		CreatedAt: now.UTC(),
		LastSeen:  now.UTC(),
		ExpiresAt: now.Add(s.ttl).UTC(),
		IP:        displayIP,
		UserAgent: truncate(ua, 300),
	}
	if err := s.db.Put(store.Sessions, sess.TokenHash, sess); err != nil {
		return "", nil, nil, err
	}
	return token, sess, &u, nil
}

// Authenticate resolves a session token.
func (s *Service) Authenticate(token string) (*Session, *User, error) {
	if token == "" || len(token) > 128 {
		return nil, nil, ErrNoSession
	}
	th := hashToken(token)
	var sess Session
	if err := s.db.Get(store.Sessions, th, &sess); err != nil {
		return nil, nil, ErrNoSession
	}
	now := time.Now()
	if now.After(sess.ExpiresAt) || now.Sub(sess.LastSeen) > s.idle {
		_ = s.db.Delete(store.Sessions, th)
		return nil, nil, ErrNoSession
	}
	u, err := s.userByID(sess.UserID)
	if err != nil {
		_ = s.db.Delete(store.Sessions, th)
		return nil, nil, ErrNoSession
	}
	if now.Sub(sess.LastSeen) > time.Minute {
		sess.LastSeen = now.UTC()
		_ = s.db.Put(store.Sessions, th, &sess)
	}
	return &sess, u, nil
}

func (s *Service) userByID(id string) (*User, error) {
	users, err := store.List[User](s.db, store.Users, "")
	if err != nil {
		return nil, err
	}
	for i := range users {
		if users[i].ID == id {
			return &users[i], nil
		}
	}
	return nil, store.ErrNotFound
}

func (s *Service) Logout(token string) {
	if token != "" {
		_ = s.db.Delete(store.Sessions, hashToken(token))
	}
}

func (s *Service) ChangePassword(ctx context.Context, user *User, current, next string, keepSession string) error {
	ok, err := s.verify(ctx, user.PassHash, current)
	if err != nil || !ok {
		return ErrWrongPassword
	}
	if err := CheckPasswordPolicy(next); err != nil {
		return err
	}
	if current == next {
		return apperr.Bad("новый пароль совпадает с текущим")
	}
	hash, err := HashPassword(next)
	if err != nil {
		return err
	}
	if _, err := store.Modify(s.db, store.Users, userKey(user.Username), func(u *User) error {
		u.PassHash = hash
		u.PasswordChangedAt = time.Now().UTC()
		u.MustChangePassword = false
		return nil
	}); err != nil {
		return err
	}
	s.revokeAll(user.ID, keepSession)
	return nil
}

// ChangeUsername renames the account after checking the current password.
func (s *Service) ChangeUsername(ctx context.Context, user *User, next, password string) error {
	next = strings.TrimSpace(next)
	if !usernameRe.MatchString(next) {
		return apperr.Bad("логин: от 3 до 64 символов — латиница, цифры, точка, дефис, подчёркивание")
	}
	ok, err := s.verify(ctx, user.PassHash, password)
	if err != nil || !ok {
		return ErrWrongPassword
	}
	if userKey(next) == userKey(user.Username) {
		_, err := store.Modify(s.db, store.Users, userKey(user.Username), func(u *User) error {
			u.Username = next
			return nil
		})
		return err
	}
	u := *user
	u.Username = next
	moved, err := s.db.Move(store.Users, userKey(user.Username), userKey(next), &u)
	if err != nil {
		return err
	}
	if !moved {
		return apperr.Conflict("такой логин уже занят")
	}
	return nil
}

// ResetPassword sets a new random password and ends every session. Used by
// the command line, for owners locked out of the panel. With an empty
// username the only account is reset.
func (s *Service) ResetPassword(username string, out io.Writer) error {
	if username == "" {
		users, err := store.List[User](s.db, store.Users, "")
		if err != nil {
			return err
		}
		if len(users) != 1 {
			return fmt.Errorf("укажите логин: mrsmith reset-password <логин>")
		}
		username = users[0].Username
	}
	pw := GeneratePassword()
	hash, err := HashPassword(pw)
	if err != nil {
		return err
	}
	u, err := store.Modify(s.db, store.Users, userKey(username), func(u *User) error {
		u.PassHash = hash
		u.PasswordChangedAt = time.Now().UTC()
		u.MustChangePassword = true
		return nil
	})
	if err != nil {
		return fmt.Errorf("пользователь %q не найден", username)
	}
	s.revokeAll(u.ID, "")
	printCredentials(out, "пароль сброшен, все сессии завершены", u.Username, pw)
	return nil
}

func (s *Service) Sessions(userID string) ([]Session, error) {
	all, err := store.List[Session](s.db, store.Sessions, "")
	if err != nil {
		return nil, err
	}
	out := all[:0]
	for _, x := range all {
		if x.UserID == userID {
			out = append(out, x)
		}
	}
	return out, nil
}

func (s *Service) Revoke(userID, sessionID string) error {
	all, err := store.List[Session](s.db, store.Sessions, "")
	if err != nil {
		return err
	}
	for _, x := range all {
		if x.UserID == userID && x.ID == sessionID {
			return s.db.Delete(store.Sessions, x.TokenHash)
		}
	}
	return store.ErrNotFound
}

func (s *Service) revokeAll(userID, keepSessionID string) {
	all, _ := store.List[Session](s.db, store.Sessions, "")
	for _, x := range all {
		if x.UserID == userID && x.ID != keepSessionID {
			_ = s.db.Delete(store.Sessions, x.TokenHash)
		}
	}
}

// Cleanup removes expired sessions and stale limiter entries.
func (s *Service) Cleanup() {
	now := time.Now()
	all, _ := store.List[Session](s.db, store.Sessions, "")
	for _, x := range all {
		if now.After(x.ExpiresAt) || now.Sub(x.LastSeen) > s.idle {
			_ = s.db.Delete(store.Sessions, x.TokenHash)
		}
	}
	s.byIP.sweep(now)
	s.byUser.sweep(now)
}

func randomToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func hashToken(t string) string {
	h := sha256.Sum256([]byte(t))
	return hex.EncodeToString(h[:])
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
