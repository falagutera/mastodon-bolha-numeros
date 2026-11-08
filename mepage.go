package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"
)

// accounts is the per-account part: /minhaconta (signed in) and the public
// /@user pages people turn on. nil on the server when it isn't configured.
type accounts struct {
	pages *pages
	cache *meCache
}

func (s *server) enableAccounts(pg *pages, cache *meCache, client *http.Client) {
	s.acc = &accounts{pages: pg, cache: cache}
	s.client = client
}

func (s *server) accountRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /minhaconta", func(w http.ResponseWriter, r *http.Request) { s.mePage(w, r, "pt") })
	mux.HandleFunc("GET /en/minhaconta", func(w http.ResponseWriter, r *http.Request) { s.mePage(w, r, "en") })
	mux.HandleFunc("POST /minhaconta/publica", s.handlePublic)
	mux.HandleFunc("GET /login", s.handleLogin)
	mux.HandleFunc("GET /auth/callback", s.handleCallback)
	mux.HandleFunc("POST /logout", s.handleLogout)
	// No method in these two: "GET /{acct}" would clash with the release-notes routes.
	mux.HandleFunc("/{acct}", func(w http.ResponseWriter, r *http.Request) { s.publicPage(w, r, "pt", r.PathValue("acct")) })
	mux.HandleFunc("/en/{acct}", func(w http.ResponseWriter, r *http.Request) { s.publicPage(w, r, "en", r.PathValue("acct")) })
}

// accountPath: the pages that carry forms and a session (their own CSP).
func accountPath(p string) bool {
	p = strings.TrimPrefix(p, "/en")
	return p == "/minhaconta" || strings.HasPrefix(p, "/minhaconta/") || strings.HasPrefix(p, "/@") ||
		p == "/login" || p == "/logout" || strings.HasPrefix(p, "/auth/")
}

type meTile struct {
	Label  string
	Values []kv // per period
}

type meView struct {
	Lang, Other, OtherHref string
	T                      map[string]string
	Version, Name          string
	ParentName, ParentURL  string

	Public   bool // the public /@user page, not /minhaconta
	LoggedIn bool
	Error    string

	Acct, DisplayName, Avatar, ProfileURL string
	PageOn, CanPublic                     bool
	PageURL, CSRF                         string
	Updated                               string
	Totals                                []tile
	Periods                               []kv
	Counts                                []meTile
	Talkers                               []rankPeriod
	TalkersTitle                          string
	Links                                 []Link
	LinkSubs                              []string
}

func (s *server) baseMeView(lang string) meView {
	t := s.texts(lang)
	other, href := "EN", "/en/minhaconta"
	if lang == "en" {
		other, href = "PT", "/minhaconta"
	}
	return meView{Lang: lang, T: t, Other: other, OtherHref: href, Version: version, Name: s.cfg.name,
		ParentName: s.cfg.parentName, ParentURL: s.cfg.parentURL}
}

func (s *server) mePage(w http.ResponseWriter, r *http.Request, lang string) {
	v := s.baseMeView(lang)
	sess, cookie, ok := s.currentSession(r)
	if !ok {
		s.renderMe(w, http.StatusOK, v)
		return
	}
	m, err := s.acc.cache.get(r.Context(), sess.ID, meFresh)
	if err != nil {
		if errors.Is(err, errNoAccount) {
			clearCookie(w, sessionCookie)
			s.meError(w, lang, http.StatusForbidden, "gone")
			return
		}
		log.Printf("minhaconta %d: %v", sess.ID, err)
		s.meError(w, lang, http.StatusServiceUnavailable, "busy")
		return
	}
	if err := s.acc.pages.rename(m.ID, m.User); err != nil {
		log.Printf("pages: %v", err)
	}
	v.LoggedIn, v.CSRF = true, s.csrf(cookie)
	v.PageOn, v.CanPublic = s.acc.pages.on(m.ID), m.CanPublic
	v.PageURL = s.cfg.publicURL + "/@" + m.User
	if lang == "en" {
		v.PageURL = s.cfg.publicURL + "/en/@" + m.User
	}
	s.fill(&v, m, false, time.Now())
	s.renderMe(w, http.StatusOK, v)
}

func (s *server) publicPage(w http.ResponseWriter, r *http.Request, lang, acct string) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	user, ok := strings.CutPrefix(acct, "@")
	if !ok || user == "" || strings.Contains(user, "@") {
		http.NotFound(w, r)
		return
	}
	id, ok := s.acc.pages.lookup(user)
	if !ok {
		http.NotFound(w, r)
		return
	}
	m, err := s.acc.cache.get(r.Context(), id, publicFresh)
	if errors.Is(err, errNoAccount) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		log.Printf("public page %d: %v", id, err)
		s.meError(w, lang, http.StatusServiceUnavailable, "busy")
		return
	}
	// Renamed, silenced, #nonumeros since: no page, whatever the setting says.
	if !m.CanPublic || !strings.EqualFold(m.User, user) {
		http.NotFound(w, r)
		return
	}
	v := s.baseMeView(lang)
	v.Public = true
	v.Other, v.OtherHref = "EN", "/en/@"+m.User
	if lang == "en" {
		v.Other, v.OtherHref = "PT", "/@"+m.User
	}
	s.fill(&v, m, true, time.Now())
	s.renderMe(w, http.StatusOK, v)
}

// fill turns an account's numbers into the page; public leaves out what only
// the person should see.
func (s *server) fill(v *meView, m *Me, public bool, now time.Time) {
	t, lang := v.T, v.Lang
	n := func(x int64) string { return formatInt(x, lang) }
	v.Acct, v.DisplayName, v.Avatar, v.ProfileURL = "@"+m.User, m.Name, m.Avatar, m.URL
	v.Updated = fmt.Sprintf(t["me_updated"], m.At.In(s.cfg.loc).Format("15:04"))
	graph := !(public && m.HidesGraph)
	v.Totals = []tile{{t["posts"], n(m.Posts), t["me_total"]}}
	if graph {
		v.Totals = append(v.Totals, tile{t["me_followers"], n(m.Followers), ""}, tile{t["me_following"], n(m.Following), ""})
	}
	v.Totals = append(v.Totals, tile{t["me_since"], s.month(m.Since, lang), age(m.Since, now, lang)})
	for _, p := range periods {
		v.Periods = append(v.Periods, kv{p, t["p_"+p]})
	}
	type metric struct {
		key   string
		value func(MeCounts) int64
		graph bool
	}
	for _, mt := range []metric{
		{"c_posts", func(c MeCounts) int64 { return c.Posts }, false},
		{"c_boosts_got", func(c MeCounts) int64 { return c.BoostsGot }, false},
		{"c_favs_got", func(c MeCounts) int64 { return c.FavsGot }, false},
		{"c_mentions", func(c MeCounts) int64 { return c.Mentions }, false},
		{"c_boosts_given", func(c MeCounts) int64 { return c.BoostsGiven }, false},
		{"c_favs_given", func(c MeCounts) int64 { return c.FavsGiven }, false},
		{"c_followers", func(c MeCounts) int64 { return c.Followers }, true},
		{"c_following", func(c MeCounts) int64 { return c.Following }, true},
	} {
		if mt.graph && !graph {
			continue
		}
		tl := meTile{Label: t[mt.key]}
		for _, p := range periods {
			tl.Values = append(tl.Values, kv{p, n(mt.value(m.Periods[p]))})
		}
		v.Counts = append(v.Counts, tl)
	}
	v.TalkersTitle = t["me_talkers"]
	if public {
		v.TalkersTitle = fmt.Sprintf(t["pub_talkers"], "@"+m.User)
	}
	for _, p := range periods {
		rp := rankPeriod{Key: p}
		top := topTalkers(m.Talkers, p, public, 10)
		for i, tk := range top {
			rp.Rows = append(rp.Rows, rankRow{N: i + 1, Name: tk.Name, Acct: "@" + tk.Acct, URL: tk.URL, Avatar: tk.Avatar,
				Value: n(tk.Counts[p]), Bar: pct(tk.Counts[p], top[0].Counts[p])})
		}
		v.Talkers = append(v.Talkers, rp)
	}
	for _, l := range shownLinks(m.Links, public, 12) {
		v.Links = append(v.Links, l.Link)
		v.LinkSubs = append(v.LinkSubs, s.ago(l.At, now, lang))
	}
}

func (s *server) handlePublic(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	lang := pickLang(r.PostForm.Get("lang"))
	sess, cookie, ok := s.currentSession(r)
	if !ok {
		http.Redirect(w, r, meHref(lang), http.StatusSeeOther)
		return
	}
	if !s.formOK(r, cookie) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	on := r.PostForm.Get("on") == "1"
	if on {
		// The page needs an account that may have one, and its current name.
		m, err := s.acc.cache.get(r.Context(), sess.ID, meFresh)
		if err != nil || !m.CanPublic {
			s.meError(w, lang, http.StatusForbidden, "cannot")
			return
		}
		if err := s.acc.pages.set(m.ID, m.User, true, time.Now()); err != nil {
			log.Printf("pages: %v", err)
			s.meError(w, lang, http.StatusInternalServerError, "save")
			return
		}
	} else if err := s.acc.pages.set(sess.ID, "", false, time.Now()); err != nil {
		log.Printf("pages: %v", err)
		s.meError(w, lang, http.StatusInternalServerError, "save")
		return
	}
	http.Redirect(w, r, meHref(lang), http.StatusSeeOther)
}

func (s *server) meError(w http.ResponseWriter, lang string, status int, key string) {
	v := s.baseMeView(lang)
	v.Error = v.T["err_"+key]
	s.renderMe(w, status, v)
}

func (s *server) renderMe(w http.ResponseWriter, status int, v meView) {
	var b bytes.Buffer
	if err := s.tmpl.ExecuteTemplate(&b, "me.html", v); err != nil {
		log.Printf("render me: %v", err)
		http.Error(w, "render error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if v.Public && v.Error == "" {
		w.Header().Set("Cache-Control", "public, max-age=60")
	} else {
		w.Header().Set("Cache-Control", "private, no-store")
	}
	w.WriteHeader(status)
	w.Write(b.Bytes())
}

// meCompute is what the cache runs: the store's numbers for one account.
func meCompute(st *store, cfg config) func(ctx context.Context, id int64) (*Me, error) {
	b := &bounds{}
	return func(ctx context.Context, id int64) (*Me, error) {
		return st.me(ctx, id, time.Now(), cfg, b)
	}
}
