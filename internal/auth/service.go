package auth

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"zipmanga/internal/db"

	"golang.org/x/crypto/bcrypt"
)

var ErrInvalidCredentials = errors.New("invalid username or password")

type Service struct {
	store      *db.Store
	sessionTTL time.Duration
}

func NewService(store *db.Store, sessionTTL time.Duration) *Service {
	return &Service{
		store:      store,
		sessionTTL: sessionTTL,
	}
}

func (s *Service) HashPassword(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if len(trimmed) < 6 {
		return "", errors.New("password must be at least 6 characters")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(trimmed), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(hash), nil
}

func (s *Service) VerifyPassword(hash, raw string) error {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(strings.TrimSpace(raw)))
}

func (s *Service) EnsureBootstrapAdmin(username, password string) error {
	n, err := s.store.CountUsers()
	if err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	hash, err := s.HashPassword(password)
	if err != nil {
		return err
	}
	_, err = s.store.CreateUser(strings.TrimSpace(username), hash, true)
	return err
}

func (s *Service) CreateUser(username, password string, isAdmin bool) (db.User, error) {
	username = strings.TrimSpace(username)
	if len(username) < 3 {
		return db.User{}, errors.New("username must be at least 3 characters")
	}
	hash, err := s.HashPassword(password)
	if err != nil {
		return db.User{}, err
	}
	return s.store.CreateUser(username, hash, isAdmin)
}

func (s *Service) Login(username, password string) (string, db.User, error) {
	u, err := s.store.GetUserByUsername(strings.TrimSpace(username))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", db.User{}, ErrInvalidCredentials
		}
		return "", db.User{}, err
	}
	if err := s.VerifyPassword(u.PasswordHash, password); err != nil {
		return "", db.User{}, ErrInvalidCredentials
	}

	token, err := generateToken(32)
	if err != nil {
		return "", db.User{}, err
	}
	expires := time.Now().Add(s.sessionTTL)
	if err := s.store.CreateSession(token, u.ID, expires); err != nil {
		return "", db.User{}, err
	}
	return token, u, nil
}

func (s *Service) AuthByToken(token string) (db.User, error) {
	user, _, _, err := s.AuthByTokenWithRefresh(token)
	return user, err
}

func (s *Service) AuthByTokenWithRefresh(token string) (db.User, time.Time, bool, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return db.User{}, time.Time{}, false, sql.ErrNoRows
	}
	session, err := s.store.GetSessionWithUser(token)
	if err != nil {
		return db.User{}, time.Time{}, false, err
	}
	now := time.Now()
	if now.After(session.ExpiresAt) {
		_ = s.store.DeleteSession(token)
		return db.User{}, time.Time{}, false, sql.ErrNoRows
	}

	if s.sessionTTL > 0 {
		newExpires := now.Add(s.sessionTTL)
		if newExpires.After(session.ExpiresAt) {
			if err := s.store.UpdateSessionExpires(token, newExpires); err == nil {
				return session.User, newExpires, true, nil
			}
			return db.User{}, time.Time{}, false, err
		}
	}

	return session.User, session.ExpiresAt, false, nil
}

func (s *Service) Logout(token string) error {
	return s.store.DeleteSession(token)
}

func (s *Service) CleanupExpiredSessions() error {
	_, err := s.store.DeleteExpiredSessions(time.Now())
	return err
}

func generateToken(n int) (string, error) {
	if n <= 0 {
		return "", fmt.Errorf("invalid token length: %d", n)
	}
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
