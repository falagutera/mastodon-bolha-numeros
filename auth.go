package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Sign in with the instance (OAuth 2 with PKCE, the same flow as circles). The
// token is only used to learn who the person is and is revoked right away: the
// session is a signed cookie with the account id, and numeros never acts on
// the account.

const (
	sessionCookie = "__Host-numeros"
	stateCookie   = "__Host-numeros-state"
	stateTTL      = 10 * time.Minute
	sessionTTL    = 4 * time.Hour
	oauthScope    = "profile"
)

type session struct {
	ID      int64     `json:"i"`
	User    string    `json:"u"`
	Expires time.Time `json:"e"`
}

type loginState struct {
	State    string    `json:"s"`
	Verifier string    `json:"v"`
	Lang     string    `json:"l"`
	Expires  time.Time `json:"e"`
}

// seal and open: base64(payload).base64(hmac). The purpose is part of the
// HMAC, so one cookie can't stand in for another.
func (s *server) seal(purpose string, v any) (string, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	payload := base64.RawURLEncoding.EncodeToString(raw)
	return payload + "." + s.sign(purpose, payload), nil
}

func (s *server) open(purpose, value string, v any) error {
	payload, sig, ok := strings.Cut(value, ".")
	if !ok || !hmac.Equal([]byte(sig), []byte(s.sign(purpose, payload))) {
		return errors.New("bad signature")
	}
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, v)
}

func (s *server) sign(purpose, payload string) string {
	m := hmac.New(sha256.New, s.cfg.secret)
	m.Write([]byte(purpose + "\x00" + payload))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

func setCookie(w http.ResponseWriter, name, value string, ttl time.Duration) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: "/", MaxAge: int(ttl.Seconds()),
		// Lax: the cookie must survive the top-level redirect back from the instance.
		HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode})
}

func clearCookie(w http.ResponseWriter, name string) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode})
}

// currentSession: the request's valid session, if any.
func (s *server) currentSession(r *http.Request) (session, string, bool) {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return session{}, "", false
	}
	var sess session
	if err := s.open("session", c.Value, &sess); err != nil || time.Now().After(sess.Expires) || sess.ID == 0 {
		return session{}, "", false
	}
	return sess, c.Value, true
}

// csrf: the token the forms carry, tied to the session cookie.
func (s *server) csrf(cookie string) string { return s.sign("csrf", cookie) }

// sameSite: a form post must come from our own pages. The token is the real
// check; Sec-Fetch-Site (sent by current browsers) is a second one. Not
// Origin: Referrer-Policy no-referrer makes browsers send "Origin: null".
func (s *server) formOK(r *http.Request, cookie string) bool {
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" {
		return false
	}
	return hmac.Equal([]byte(r.PostForm.Get("t")), []byte(s.csrf(cookie)))
}

func randomToken() string {
	b := make([]byte, 32)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func (s *server) redirectURI() string { return s.cfg.publicURL + "/auth/callback" }

func (s *server) handleLogin(w http.ResponseWriter, r *http.Request) {
	st := loginState{State: randomToken(), Verifier: randomToken(), Lang: pickLang(r.URL.Query().Get("lang")), Expires: time.Now().Add(stateTTL)}
	value, err := s.seal("state", st)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	setCookie(w, stateCookie, value, stateTTL)
	challenge := sha256.Sum256([]byte(st.Verifier))
	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {s.cfg.clientID},
		"redirect_uri":          {s.redirectURI()},
		"scope":                 {oauthScope},
		"state":                 {st.State},
		"code_challenge":        {base64.RawURLEncoding.EncodeToString(challenge[:])},
		"code_challenge_method": {"S256"},
	}
	http.Redirect(w, r, s.cfg.instance+"/oauth/authorize?"+q.Encode(), http.StatusFound)
}

func pickLang(l string) string {
	if l == "en" {
		return "en"
	}
	return "pt"
}

func meHref(lang string) string {
	if lang == "en" {
		return "/en/minhaconta"
	}
	return "/minhaconta"
}

func (s *server) handleCallback(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie(stateCookie)
	if err != nil {
		s.meError(w, "pt", http.StatusBadRequest, "expired")
		return
	}
	clearCookie(w, stateCookie)
	var st loginState
	if err := s.open("state", c.Value, &st); err != nil || time.Now().After(st.Expires) {
		s.meError(w, "pt", http.StatusBadRequest, "expired")
		return
	}
	lang := pickLang(st.Lang)
	q := r.URL.Query()
	if !hmac.Equal([]byte(q.Get("state")), []byte(st.State)) {
		s.meError(w, lang, http.StatusBadRequest, "expired")
		return
	}
	if q.Get("code") == "" { // declined on the instance's consent screen
		s.meError(w, lang, http.StatusForbidden, "denied")
		return
	}
	token, err := s.exchangeCode(q.Get("code"), st.Verifier)
	if err != nil {
		log.Printf("token exchange: %v", err)
		s.meError(w, lang, http.StatusBadGateway, "instance")
		return
	}
	// The token is only needed for this request, so it never outlives it.
	defer s.revokeToken(token)
	id, user, err := s.fetchAccount(token)
	if err != nil {
		log.Printf("verify_credentials: %v", err)
		s.meError(w, lang, http.StatusBadGateway, "instance")
		return
	}
	value, err := s.seal("session", session{ID: id, User: user, Expires: time.Now().Add(sessionTTL)})
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	setCookie(w, sessionCookie, value, sessionTTL)
	http.Redirect(w, r, meHref(lang), http.StatusFound)
}

func (s *server) handleLogout(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	lang := pickLang(r.PostForm.Get("lang"))
	if _, cookie, ok := s.currentSession(r); ok {
		if !s.formOK(r, cookie) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
	}
	clearCookie(w, sessionCookie)
	http.Redirect(w, r, meHref(lang), http.StatusSeeOther)
}

func (s *server) exchangeCode(code, verifier string) (string, error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"client_id":     {s.cfg.clientID},
		"client_secret": {s.cfg.clientSecret},
		"redirect_uri":  {s.redirectURI()},
		"code_verifier": {verifier},
		"scope":         {oauthScope},
	}
	resp, err := s.client.PostForm(s.cfg.instance+"/oauth/token", form)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("status %d", resp.StatusCode)
	}
	var out struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&out); err != nil {
		return "", err
	}
	if out.AccessToken == "" {
		return "", errors.New("empty access token")
	}
	return out.AccessToken, nil
}

func (s *server) fetchAccount(token string) (int64, string, error) {
	req, err := http.NewRequest(http.MethodGet, s.cfg.instance+"/api/v1/accounts/verify_credentials", nil)
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := s.client.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, "", fmt.Errorf("status %d", resp.StatusCode)
	}
	var out struct {
		ID       string `json:"id"`
		Username string `json:"username"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return 0, "", err
	}
	id, err := strconv.ParseInt(out.ID, 10, 64)
	if err != nil || id <= 0 || out.Username == "" {
		return 0, "", errors.New("no account in the answer")
	}
	return id, out.Username, nil
}

func (s *server) revokeToken(token string) {
	resp, err := s.client.PostForm(s.cfg.instance+"/oauth/revoke", url.Values{
		"client_id": {s.cfg.clientID}, "client_secret": {s.cfg.clientSecret}, "token": {token},
	})
	if err != nil {
		log.Printf("revoke: %v", err)
		return
	}
	resp.Body.Close()
}
