package adapters

import (
	"context"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/ex-business/ExCommission/internal/identity/domain"
	"github.com/ex-business/ExCommission/internal/identity/ports"
)

type LoginAccount struct {
	Username  string           `json:"username"`
	Salt      string           `json:"salt"`
	Hash      string           `json:"hash"`
	Principal domain.Principal `json:"-"`
}
type loginSession struct {
	Principal domain.Principal
	Expires   time.Time
}
type loginWindow struct {
	Start time.Time
	Count int
}

var ErrLoginRate = errors.New("LOGIN_RATE_LIMITED")

// Sessions is a bounded single-instance store; restart invalidates issued sessions.
type Sessions struct {
	mu       sync.Mutex
	accounts map[string]LoginAccount
	sessions map[[32]byte]loginSession
	attempts map[string]loginWindow
	Now      func() time.Time
	Legacy   ports.Provider
	External ports.Provider
}

func NewSessions(accounts []LoginAccount, legacy, external ports.Provider) (*Sessions, error) {
	s := &Sessions{accounts: map[string]LoginAccount{}, sessions: map[[32]byte]loginSession{}, attempts: map[string]loginWindow{}, Now: time.Now, Legacy: legacy, External: external}
	for _, a := range accounts {
		salt, e := hex.DecodeString(a.Salt)
		hash, e2 := hex.DecodeString(a.Hash)
		if e != nil || e2 != nil || len(salt) != 16 || len(hash) != 32 || a.Username == "" || a.Principal.Subject == "" {
			return nil, errors.New("invalid login account configuration")
		}
		if _, ok := s.accounts[a.Username]; ok {
			return nil, errors.New("duplicate login account")
		}
		s.accounts[a.Username] = a
	}
	return s, nil
}
func (s *Sessions) Login(username, password string) (string, domain.Principal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.Now()
	// Unknown names share a fixed bucket so input cannot grow the limiter map.
	key := username
	if _, ok := s.accounts[key]; !ok {
		key = "unknown"
	}
	for _, k := range []string{"global", "user:" + key} {
		w := s.attempts[k]
		if now.Sub(w.Start) >= time.Minute {
			w = loginWindow{Start: now}
		}
		w.Count++
		s.attempts[k] = w
		if (k == "global" && w.Count > 60) || (k != "global" && w.Count > 10) {
			return "", domain.Principal{}, ErrLoginRate
		}
	}
	a, ok := s.accounts[username]
	salt, _ := hex.DecodeString(a.Salt)
	expected, _ := hex.DecodeString(a.Hash)
	if !ok {
		salt = make([]byte, 16)
		expected = make([]byte, 32)
	}
	actual, err := pbkdf2.Key(sha256.New, password, salt, 600000, 32)
	if err != nil {
		return "", domain.Principal{}, err
	}
	if subtle.ConstantTimeCompare(actual, expected) != 1 || !ok {
		return "", domain.Principal{}, domain.ErrUnauthenticated
	}
	for h, v := range s.sessions {
		if !now.Before(v.Expires) {
			delete(s.sessions, h)
		}
	}
	if len(s.sessions) >= 10000 {
		return "", domain.Principal{}, ErrLoginRate
	}
	b := make([]byte, 32)
	if _, err = rand.Read(b); err != nil {
		return "", domain.Principal{}, err
	}
	token := "lc_" + hex.EncodeToString(b)
	s.sessions[sha256.Sum256([]byte(token))] = loginSession{a.Principal, now.Add(8 * time.Hour)}
	return token, a.Principal, nil
}
func (s *Sessions) Authenticate(ctx context.Context, header string) (domain.Principal, error) {
	if !strings.HasPrefix(header, "Bearer ") || len(header) > 8192 {
		return domain.Principal{}, domain.ErrUnauthenticated
	}
	token := strings.TrimPrefix(header, "Bearer ")
	if strings.HasPrefix(token, "lc_") {
		s.mu.Lock()
		defer s.mu.Unlock()
		h := sha256.Sum256([]byte(token))
		v, ok := s.sessions[h]
		if !ok || !s.Now().Before(v.Expires) {
			delete(s.sessions, h)
			return domain.Principal{}, domain.ErrUnauthenticated
		}
		return v.Principal, nil
	}
	if s.Legacy != nil {
		p, err := s.Legacy.Authenticate(ctx, header)
		if err == nil {
			return p, nil
		}
		if !errors.Is(err, domain.ErrUnauthenticated) {
			return p, err
		}
	}
	if s.External != nil {
		return s.External.Authenticate(ctx, header)
	}
	return domain.Principal{}, domain.ErrUnauthenticated
}
func (s *Sessions) Logout(header string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, sha256.Sum256([]byte(strings.TrimPrefix(header, "Bearer "))))
}
