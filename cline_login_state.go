package main

import (
	"errors"
	"net/http"
	"net/url"
	"time"
)

type clineLoginAttempt struct {
	Session     string
	RedirectURI string
	ExpiresAt   time.Time
}

func (s *Server) beginClineLogin(r *http.Request) (string, string) {
	now := time.Now()
	state := newSessionID()
	callback, _ := url.Parse(publicURL(r, "/api/oauth/cline/callback"))
	query := callback.Query()
	query.Set("login_state", state)
	callback.RawQuery = query.Encode()
	s.clineLoginMu.Lock()
	defer s.clineLoginMu.Unlock()
	if s.clineLogins == nil {
		s.clineLogins = map[string]clineLoginAttempt{}
	}
	for key, attempt := range s.clineLogins {
		if !now.Before(attempt.ExpiresAt) {
			delete(s.clineLogins, key)
		}
	}
	s.clineLogins[state] = clineLoginAttempt{Session: s.adminCookieValue(), RedirectURI: callback.String(), ExpiresAt: now.Add(10 * time.Minute)}
	return state, callback.String()
}

func (s *Server) consumeClineLogin(r *http.Request) (string, error) {
	state := r.URL.Query().Get("login_state")
	if state == "" {
		state = r.URL.Query().Get("state")
	}
	s.clineLoginMu.Lock()
	defer s.clineLoginMu.Unlock()
	attempt, found := s.clineLogins[state]
	if !found || !time.Now().Before(attempt.ExpiresAt) || attempt.Session != s.adminCookieValue() {
		return "", errors.New("Cline 登录请求无效或已过期，请从管理页重新发起登录")
	}
	delete(s.clineLogins, state)
	return attempt.RedirectURI, nil
}
