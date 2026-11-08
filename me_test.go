package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestPagesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "public.json")
	p, err := openPages(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := p.set(7, "Ana", true, now); err != nil {
		t.Fatal(err)
	}
	if id, ok := p.lookup("ana"); !ok || id != 7 {
		t.Errorf("lookup is case-insensitive: %d %v", id, ok)
	}
	if err := p.rename(7, "ana2"); err != nil {
		t.Fatal(err)
	}
	if _, ok := p.lookup("ana"); ok {
		t.Error("old name still resolves")
	}
	again, err := openPages(path)
	if err != nil {
		t.Fatal(err)
	}
	if id, ok := again.lookup("ANA2"); !ok || id != 7 || !again.on(7) {
		t.Errorf("not kept in the file: %d %v", id, ok)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Errorf("file mode %v", fi.Mode().Perm())
	}
	if err := again.set(7, "", false, now); err != nil {
		t.Fatal(err)
	}
	if again.on(7) || again.count() != 0 {
		t.Error("still on")
	}
	// ana renames to "ana3"; bia takes the name "ana" and opens her page. ana's
	// next visit (rename) must not take bia's page down.
	if err := again.set(7, "ana", true, now); err != nil {
		t.Fatal(err)
	}
	again.byID[7] = pageEntry{User: "ana", Since: now} // as stored before ana renamed
	if err := again.set(8, "ana", true, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := again.rename(7, "ana3"); err != nil {
		t.Fatal(err)
	}
	if id, ok := again.lookup("ana"); !ok || id != 8 {
		t.Errorf("bia's page lost after ana's rename: %d %v", id, ok)
	}
	if id, ok := again.lookup("ana3"); !ok || id != 7 {
		t.Errorf("ana's new name: %d %v", id, ok)
	}
	again.set(7, "", false, now)
	again.set(8, "", false, now)
	// A file that can't be written leaves the setting as it was.
	bad := &pages{path: filepath.Join(t.TempDir(), "missing", "x.json"), byID: map[int64]pageEntry{}, byName: map[string]int64{}}
	if err := bad.set(1, "bia", true, now); err == nil || bad.on(1) {
		t.Errorf("save error not reported or memory changed: %v", err)
	}
}

func testMe() *Me {
	counts := func(k int64) MeCounts {
		// FavsGiven 777 and the gap between Posts and PubPosts mark what the public page must not show.
		return MeCounts{Posts: k, PubPosts: 100 + k, BoostsGot: 2 * k, FavsGiven: 777, FavsGot: 4 * k, PubFavsGot: 200 + k,
			Followers: 3 * k, Following: k, Mentions: k}
	}
	return &Me{At: time.Now(), ID: 7, User: "ana", Name: "Ana", Avatar: "https://cache.example/a.png", URL: "https://inst.example/@ana",
		Since: time.Date(2022, 5, 1, 0, 0, 0, 0, time.UTC), Posts: 900, Followers: 321, Following: 12, CanPublic: true,
		Periods: map[string]MeCounts{"d": counts(1), "w": counts(5), "m": counts(20)},
		Talkers: []MeTalker{
			{Person: Person{Acct: "fala@remote.example", Name: "Fala", URL: "https://remote.example/@fala"},
				Counts: map[string]int64{"d": 1, "w": 2, "m": 9}, Public: map[string]int64{"d": 1, "w": 2, "m": 7}, Shown: true},
			{Person: Person{Acct: "segredo", Name: "Segredo", URL: "https://inst.example/@segredo"},
				Counts: map[string]int64{"d": 0, "w": 4, "m": 4}, Public: map[string]int64{"d": 0, "w": 4, "m": 4}, Shown: false},
			// Discoverable, but only ever mentioned ana in direct messages.
			{Person: Person{Acct: "sodm", Name: "SoDM", URL: "https://inst.example/@sodm"},
				Counts: map[string]int64{"d": 0, "w": 0, "m": 30}, Public: map[string]int64{"d": 0, "w": 0, "m": 0}, Shown: true},
		},
		PublicMentions: map[string]int64{"d": 1, "w": 6, "m": 11},
		Links: []MeLink{
			{Link: Link{URL: "https://pub.example/a", Title: "Publico", Domain: "pub.example", At: time.Now().Add(-time.Hour)}, Public: true},
			{Link: Link{URL: "https://priv.example/b", Title: "SoSeguidores", Domain: "priv.example", At: time.Now().Add(-time.Hour)}},
			{Link: Link{URL: "https://pub.example/a", Title: "Publico", Domain: "pub.example", At: time.Now().Add(-2 * time.Hour)}, Public: true},
		},
	}
}

func TestTopTalkersAndLinks(t *testing.T) {
	m := testMe()
	if got := topTalkers(m.Talkers, "w", false, 10); len(got) != 2 || got[0].Acct != "segredo" {
		t.Errorf("private, by the week: %+v", got)
	}
	if got := topTalkers(m.Talkers, "w", true, 10); len(got) != 1 || got[0].Acct != "fala@remote.example" {
		t.Errorf("public leaves out who can't be shown: %+v", got)
	}
	if got := topTalkers(m.Talkers, "m", false, 10); len(got) != 3 || got[0].Acct != "sodm" {
		t.Errorf("own page counts direct messages too: %+v", got)
	}
	if got := topTalkers(m.Talkers, "m", true, 10); len(got) != 1 || got[0].Acct != "fala@remote.example" || got[0].count("m", true) != 7 {
		t.Errorf("public page: only mentions in public posts: %+v", got)
	}
	if got := topTalkers(m.Talkers, "d", false, 10); len(got) != 1 {
		t.Errorf("zero counts stay out: %+v", got)
	}
	if got := shownLinks(m.Links, false, 12); len(got) != 2 {
		t.Errorf("private links, one per URL: %+v", got)
	}
	if got := shownLinks(m.Links, true, 12); len(got) != 1 || got[0].URL != "https://pub.example/a" {
		t.Errorf("public links: %+v", got)
	}
}

func TestMeCache(t *testing.T) {
	calls := 0
	var fail error
	c := newMeCache(func(ctx context.Context, id int64) (*Me, error) {
		calls++
		if fail != nil {
			return nil, fail
		}
		m := testMe()
		m.At = c0now
		return m, nil
	})
	now := c0now
	c.now = func() time.Time { return now }
	ctx := context.Background()
	if _, err := c.get(ctx, 7, meFresh, meStale); err != nil || calls != 1 {
		t.Fatalf("first: %v %d", err, calls)
	}
	c.get(ctx, 7, meFresh, meStale)
	if calls != 1 {
		t.Error("not cached")
	}
	now = now.Add(6 * time.Minute)
	c.get(ctx, 7, publicFresh, publicFresh) // public pages take older numbers
	if calls != 1 {
		t.Error("public page recomputed too soon")
	}
	c.get(ctx, 7, meFresh, meStale) // computes again (stamped c0now: 6 min old)
	if calls != 2 {
		t.Error("own page not recomputed after 5 minutes")
	}
	now = now.Add(20 * time.Minute)
	fail = context.DeadlineExceeded
	if m, err := c.get(ctx, 7, meFresh, meStale); err != nil || m == nil {
		t.Errorf("own page: a failure should fall back on numbers under an hour old: %v", err)
	}
	// A public page never shows numbers past publicFresh: an opt-out in them
	// would go unseen while the database is slow.
	if m, err := c.get(ctx, 7, publicFresh, publicFresh); err == nil || m != nil {
		t.Error("public page served stale numbers after a failure")
	}
	now = now.Add(2 * time.Hour)
	if _, err := c.get(ctx, 7, meFresh, meStale); err == nil {
		t.Error("own page served numbers over an hour old")
	}
	fail = errNoAccount
	if _, err := c.get(ctx, 7, meFresh, meStale); err != errNoAccount || c.cached(7) != nil {
		t.Errorf("a gone account is forgotten: %v", err)
	}
	before := calls
	for i := 0; i < 50; i++ { // a stale /@name hit over and over
		c.get(ctx, 7, publicFresh, publicFresh)
	}
	if calls != before {
		t.Errorf("a gone account was looked up %d more times", calls-before)
	}
	now = now.Add(publicFresh + time.Minute)
	fail = nil
	if m, err := c.get(ctx, 7, publicFresh, publicFresh); err != nil || m == nil {
		t.Errorf("an account that came back (unsuspended) stays gone: %v", err)
	}
}

var c0now = time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)

// fakeInstance: the OAuth endpoints of an instance that knows one account.
func fakeInstance(t *testing.T, revoked *int) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth/token":
			r.ParseForm()
			if r.PostForm.Get("code") != "good" || r.PostForm.Get("code_verifier") == "" {
				http.Error(w, "bad", http.StatusBadRequest)
				return
			}
			json.NewEncoder(w).Encode(map[string]string{"access_token": "tok"})
		case "/api/v1/accounts/verify_credentials":
			if r.Header.Get("Authorization") != "Bearer tok" {
				http.Error(w, "no", http.StatusUnauthorized)
				return
			}
			json.NewEncoder(w).Encode(map[string]string{"id": "7", "username": "ana"})
		case "/oauth/revoke":
			*revoked++
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestAccountPages(t *testing.T) {
	revoked := 0
	inst := fakeInstance(t, &revoked)
	defer inst.Close()
	cfg := testConfig()
	cfg.instance, cfg.publicURL = inst.URL, "https://numeros.example"
	cfg.clientID, cfg.clientSecret, cfg.secret = "cid", "csecret", []byte(strings.Repeat("k", 32))
	col := &collector{cfg: cfg, now: time.Now}
	srv, err := newServer(cfg, col)
	if err != nil {
		t.Fatal(err)
	}
	pg, _ := openPages(filepath.Join(t.TempDir(), "public.json"))
	me := testMe()
	srv.enableAccounts(pg, newMeCache(func(ctx context.Context, id int64) (*Me, error) {
		if id != 7 {
			return nil, errNoAccount
		}
		m := *me
		m.At = time.Now()
		return &m, nil
	}), inst.Client())
	ts := httptest.NewServer(srv.routes())
	defer ts.Close()
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(method, path, cookie string, form url.Values) (*http.Response, string) {
		t.Helper()
		var body io.Reader
		if form != nil {
			body = strings.NewReader(form.Encode())
		}
		req, _ := http.NewRequest(method, ts.URL+path, body)
		if form != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		if cookie != "" {
			req.Header.Set("Cookie", cookie)
		}
		res, err := noRedirect.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(res.Body)
		res.Body.Close()
		return res, string(b)
	}
	cookieOf := func(res *http.Response, name string) string {
		for _, c := range res.Cookies() {
			if c.Name == name && c.MaxAge > 0 {
				if !c.Secure || !c.HttpOnly {
					t.Errorf("cookie %s not Secure/HttpOnly", name)
				}
				return name + "=" + c.Value
			}
		}
		return ""
	}

	// The main page links to /minhaconta and keeps its own strict CSP.
	res, body := do("GET", "/", "", nil)
	if !strings.Contains(body, `href="/minhaconta"`) || !strings.Contains(res.Header.Get("Content-Security-Policy"), "form-action 'none'") {
		t.Errorf("main page: link or CSP: %s", res.Header.Get("Content-Security-Policy"))
	}
	// Signed out: a sign-in button, nothing else.
	res, body = do("GET", "/minhaconta", "", nil)
	if res.StatusCode != 200 || !strings.Contains(body, `href="/login?lang=pt"`) || strings.Contains(body, "Ana") {
		t.Errorf("signed out page: %d", res.StatusCode)
	}
	if csp := res.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "form-action 'self'") || !strings.Contains(csp, "frame-ancestors 'none'") {
		t.Errorf("account CSP: %s", csp)
	}
	if cc := res.Header.Get("Cache-Control"); !strings.Contains(cc, "no-store") {
		t.Errorf("cache-control: %s", cc)
	}

	// Sign in.
	res, _ = do("GET", "/login?lang=pt", "", nil)
	loc, _ := url.Parse(res.Header.Get("Location"))
	if res.StatusCode != http.StatusFound || loc.Path != "/oauth/authorize" || loc.Query().Get("scope") != "profile" ||
		loc.Query().Get("code_challenge_method") != "S256" || loc.Query().Get("redirect_uri") != "https://numeros.example/auth/callback" {
		t.Fatalf("authorize redirect: %s", loc)
	}
	state := cookieOf(res, stateCookie)
	if res, _ := do("GET", "/auth/callback?code=good&state=wrong", state, nil); res.StatusCode != http.StatusBadRequest {
		t.Errorf("wrong state: %d", res.StatusCode)
	}
	if res, _ := do("GET", "/auth/callback?code=good&state="+loc.Query().Get("state"), "", nil); res.StatusCode != http.StatusBadRequest {
		t.Errorf("no state cookie: %d", res.StatusCode)
	}
	res, _ = do("GET", "/auth/callback?code=good&state="+loc.Query().Get("state"), state, nil)
	sess := cookieOf(res, sessionCookie)
	if res.StatusCode != http.StatusFound || res.Header.Get("Location") != "/minhaconta" || sess == "" {
		t.Fatalf("callback: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	if revoked != 1 {
		t.Errorf("token not revoked: %d", revoked)
	}
	// A forged session doesn't count.
	if _, body := do("GET", "/minhaconta", sessionCookie+"=eyJpIjo3fQ.forged", nil); strings.Contains(body, "Ana") {
		t.Error("forged session accepted")
	}

	// Signed in: own numbers, everyone who talks, every link.
	res, body = do("GET", "/minhaconta", sess, nil)
	for _, want := range []string{"Ana", "@ana", "Segredo", "SoDM", "SoSeguidores", "Abrir página pública", "321", "favs dados", ">777<"} {
		if !strings.Contains(body, want) {
			t.Errorf("own page lacks %q", want)
		}
	}
	csrf := between(body, `name="t" value="`, `"`)
	if csrf == "" {
		t.Fatal("no csrf token")
	}
	// No public page yet: 404, same as a name that doesn't exist.
	if res, _ := do("GET", "/@ana", "", nil); res.StatusCode != 404 {
		t.Errorf("public page before opting in: %d", res.StatusCode)
	}
	// Turning it on needs the form token from our page.
	if res, _ := do("POST", "/minhaconta/publica", sess, url.Values{"on": {"1"}}); res.StatusCode != http.StatusForbidden {
		t.Errorf("no token: %d", res.StatusCode)
	}
	if res, _ := do("POST", "/minhaconta/publica", sess, url.Values{"on": {"1"}, "t": {"x" + csrf}}); res.StatusCode != http.StatusForbidden {
		t.Errorf("bad token: %d", res.StatusCode)
	}
	if res, _ := do("POST", "/minhaconta/publica", "", url.Values{"on": {"1"}, "t": {csrf}}); res.StatusCode != http.StatusSeeOther || pg.on(7) {
		t.Errorf("no session: %d", res.StatusCode)
	}
	res, _ = do("POST", "/minhaconta/publica", sess, url.Values{"on": {"1"}, "t": {csrf}})
	if res.StatusCode != http.StatusSeeOther || !pg.on(7) {
		t.Fatalf("turn on: %d", res.StatusCode)
	}
	if _, body := do("GET", "/minhaconta", sess, nil); !strings.Contains(body, "https://numeros.example/@ana") {
		t.Error("own page doesn't show the public URL")
	}

	// The public page: no session, only what may be shown.
	res, body = do("GET", "/@ANA", "", nil)
	if res.StatusCode != 200 {
		t.Fatalf("public page: %d", res.StatusCode)
	}
	for _, want := range []string{"Fala", "Publico", `rel="me"`} {
		if !strings.Contains(body, want) {
			t.Errorf("public page lacks %q", want)
		}
	}
	// Counts: posts and favs received only from public posts (PubPosts 120, PubFavsGot 220
	// for 30 days); favs given are private in Mastodon and stay off.
	for _, want := range []string{">120<", ">220<"} {
		if !strings.Contains(body, want) {
			t.Errorf("public page lacks public count %s", want)
		}
	}
	for _, not := range []string{"Segredo", "SoDM", "SoSeguidores", "/logout", csrf, "favs dados", ">777<", ">80<"} {
		if strings.Contains(body, not) {
			t.Errorf("public page shows %q", not)
		}
	}
	if _, body := do("GET", "/en/@ana", "", nil); !strings.Contains(body, "Who talks to @ana the most") {
		t.Error("public page in English")
	}
	// Hiding who follows them takes the follower numbers off the public page only.
	me.HidesGraph = true
	srv.acc.cache.forget(7)
	if _, body := do("GET", "/@ana", "", nil); strings.Contains(body, "seguidores ganhos") || strings.Contains(body, ">321<") {
		t.Error("public page shows followers of someone who hides them")
	}
	if _, body := do("GET", "/minhaconta", sess, nil); !strings.Contains(body, "seguidores ganhos") {
		t.Error("own page lost the followers")
	}
	// #nonumeros (or limited, moved) since: the page is gone even with the setting on.
	me.CanPublic = false
	srv.acc.cache.forget(7)
	if res, _ := do("GET", "/@ana", "", nil); res.StatusCode != 404 {
		t.Errorf("public page of an account that can't have one: %d", res.StatusCode)
	}
	me.CanPublic = true
	srv.acc.cache.forget(7)
	for _, p := range []string{"/@bia", "/@", "/@ana@other.example", "/ana", "/en/bia"} {
		if res, _ := do("GET", p, "", nil); res.StatusCode != 404 {
			t.Errorf("%s: %d", p, res.StatusCode)
		}
	}
	// Turning it off takes it down at once.
	do("POST", "/minhaconta/publica", sess, url.Values{"on": {"0"}, "t": {csrf}})
	if res, _ := do("GET", "/@ana", "", nil); res.StatusCode != 404 || pg.on(7) {
		t.Errorf("after turning off: %d", res.StatusCode)
	}
	// Signing out needs the token too, then the session is gone.
	if res, _ := do("POST", "/logout", sess, url.Values{}); res.StatusCode != http.StatusForbidden {
		t.Errorf("logout without token: %d", res.StatusCode)
	}
	res, _ = do("POST", "/logout", sess, url.Values{"t": {csrf}})
	if res.StatusCode != http.StatusSeeOther {
		t.Errorf("logout: %d", res.StatusCode)
	}
	cleared := false
	for _, c := range res.Cookies() {
		cleared = cleared || (c.Name == sessionCookie && c.MaxAge < 0)
	}
	if !cleared {
		t.Error("session cookie not cleared")
	}
	// A declined consent and a dead instance end on a page, not a crash.
	res, _ = do("GET", "/login", "", nil)
	loc, _ = url.Parse(res.Header.Get("Location"))
	if res, _ := do("GET", "/auth/callback?state="+loc.Query().Get("state"), cookieOf(res, stateCookie), nil); res.StatusCode != http.StatusForbidden {
		t.Errorf("declined: %d", res.StatusCode)
	}
}

func TestAccountsOffByDefault(t *testing.T) {
	cfg := testConfig()
	srv, _ := newServer(cfg, &collector{cfg: cfg, now: time.Now})
	ts := httptest.NewServer(srv.routes())
	defer ts.Close()
	for _, p := range []string{"/minhaconta", "/@ana", "/login"} {
		res, err := http.Get(ts.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != 404 {
			t.Errorf("%s with sign-in off: %d", p, res.StatusCode)
		}
	}
}

func TestSignInConfig(t *testing.T) {
	base := map[string]string{"NUMEROS_DB_PASSWORD": "x", "NUMEROS_CLIENT_ID": "cid", "NUMEROS_CLIENT_SECRET": "s",
		"NUMEROS_SESSION_SECRET": strings.Repeat("k", 32), "NUMEROS_PUBLIC_URL": "https://numeros.example", "NUMEROS_PAGES_FILE": "/data/public.json"}
	set := func(over map[string]string) error {
		for k, v := range base {
			t.Setenv(k, v)
		}
		for k, v := range over {
			t.Setenv(k, v)
		}
		_, err := loadConfig()
		return err
	}
	if err := set(nil); err != nil {
		t.Errorf("good config: %v", err)
	}
	for _, bad := range []map[string]string{
		{"NUMEROS_SESSION_SECRET": "short"}, {"NUMEROS_CLIENT_SECRET": ""}, {"NUMEROS_PAGES_FILE": ""},
		{"NUMEROS_PUBLIC_URL": "http://numeros.example"}, {"NUMEROS_PUBLIC_URL": "https://numeros.example/x"},
	} {
		if err := set(bad); err == nil {
			t.Errorf("accepted %v", bad)
		}
	}
}

func between(s, a, b string) string {
	i := strings.Index(s, a)
	if i < 0 {
		return ""
	}
	s = s[i+len(a):]
	if j := strings.Index(s, b); j >= 0 {
		return s[:j]
	}
	return ""
}

func TestMeQueries(t *testing.T) {
	host := os.Getenv("NUMEROS_TEST_DB_HOST")
	if host == "" {
		t.Skip("NUMEROS_TEST_DB_HOST not set")
	}
	cfg := testConfig()
	cfg.dbHost, cfg.dbPort = host, env("NUMEROS_TEST_DB_PORT", "5432")
	cfg.dbName, cfg.dbUser = env("NUMEROS_TEST_DB_NAME", "numeros_test"), env("NUMEROS_TEST_DB_USER", "postgres")
	cfg.dbPassword, cfg.dbSSLMode = env("NUMEROS_TEST_DB_PASSWORD", "pg"), "disable"
	ctx := context.Background()
	db, err := openStore(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer db.pool.Close()
	if _, err := db.pool.Exec(ctx, testSchema); err != nil {
		t.Fatal(err)
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := db.pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	// Noon: "a minute ago" is always today, whenever the test runs.
	l := time.Now().In(cfg.loc)
	now := time.Date(l.Year(), l.Month(), l.Day(), 12, 0, 0, 0, cfg.loc)
	// ana (1) is the account; bia (2) local discoverable; caio (3) local with #nonumeros;
	// fala@remote (10) discoverable; muda@remote (11) not; eva (6) suspended; zed (4) no user row.
	exec(`INSERT INTO accounts (id, username, domain, discoverable, note, suspended_at, created_at) VALUES
		(1, 'ana', NULL, true, '', NULL, '2022-05-01'), (2, 'bia', NULL, true, '', NULL, now()),
		(3, 'caio', NULL, true, '#nonumeros', NULL, now()), (6, 'eva', NULL, true, '', now(), now()),
		(4, 'zed', NULL, true, '', NULL, now()),
		(10, 'fala', 'remote.example', true, '', NULL, now()), (11, 'muda', 'remote.example', false, '', NULL, now()),
		(12, 'tagi', 'remote.example', true, '<p>sem ranking <a href="https://remote.example/tags/nonumeros" class="mention hashtag" rel="tag">#<span>NoNumeros</span></a></p>', NULL, now())`)
	exec(`INSERT INTO users (account_id) VALUES (1), (2), (3), (6)`)
	exec(`INSERT INTO account_stats (account_id, statuses_count, followers_count, following_count) VALUES (1, 900, 321, 12)`)

	n := int64(0)
	post := func(acct int64, ago time.Duration, extra string) int64 {
		n++
		id := snowflake(now.Add(-ago)) + n
		exec(`INSERT INTO statuses (id, account_id, created_at) VALUES ($1, $2, $3)`, id, acct, now.Add(-ago).UTC())
		if extra != "" {
			exec(`UPDATE statuses SET `+extra+` WHERE id = $1`, id)
		}
		return id
	}
	// ana: 2 posts in the last hour, 1 four days ago, 1 twenty days ago, 1 sixty days ago;
	// a boost now; a deleted post and a direct message that don't count.
	post(1, time.Minute, "")
	link := post(1, 2*time.Minute, "")
	post(1, 4*24*time.Hour, "")
	cw := post(1, 20*24*time.Hour, "spoiler_text = 'cw'")
	post(1, 60*24*time.Hour, "")
	post(1, time.Minute, "reblog_of_id = 999")
	post(1, time.Minute, "deleted_at = now()")
	dm := post(1, time.Minute, "visibility = 3")
	exec(`INSERT INTO preview_cards (id, url, title) VALUES (1, 'https://www.news.example/x', 'Notícia'), (2, 'https://cw.example/y', 'Com aviso'), (3, 'https://dm.example/z', 'Na DM')`)
	exec(`INSERT INTO preview_cards_statuses VALUES (1, $1), (2, $2), (3, $3)`, link, cw, dm)

	// Notifications to ana, with old ones before the 30-day bound.
	at := func(ago time.Duration) time.Time { return now.Add(-ago).UTC() }
	for i := 0; i < 50; i++ { // padding: old rows the bound must skip
		exec(`INSERT INTO notifications (account_id, from_account_id, type, created_at) VALUES (1, 10, 'mention', $1)`, at(90*24*time.Hour))
	}
	// A mention notification points at its mentions row, which points at the post.
	mention := func(to, from int64, ago time.Duration, visibility int, filtered bool) {
		t.Helper()
		sid := post(from, ago, fmt.Sprintf("visibility = %d", visibility))
		var mid int64
		if err := db.pool.QueryRow(ctx, `INSERT INTO mentions (account_id, status_id) VALUES ($1, $2) RETURNING id`, to, sid).Scan(&mid); err != nil {
			t.Fatal(err)
		}
		exec(`INSERT INTO notifications (account_id, from_account_id, type, created_at, filtered, activity_type, activity_id)
			VALUES ($1, $2, 'mention', $3, $4, 'Mention', $5)`, to, from, at(ago), filtered, mid)
	}
	mention(1, 10, time.Minute, 3, false)     // fala, a direct message
	mention(1, 10, 10*24*time.Hour, 0, false) // fala, public
	mention(1, 11, time.Minute, 1, false)     // muda, unlisted
	mention(1, 2, 10*24*time.Hour, 2, false)  // bia, followers-only
	mention(1, 3, 10*24*time.Hour, 0, false)  // caio, public
	mention(1, 6, time.Minute, 0, false)      // eva (suspended), public
	mention(1, 10, time.Minute, 0, true)      // filtered by ana's policy
	mention(1, 2, time.Minute, 0, false)      // bia, but local-only: not for the public page
	exec(`UPDATE statuses SET local_only = true WHERE id = (SELECT max(id) FROM statuses WHERE account_id = 2)`)
	mention(1, 12, time.Minute, 0, false) // tagi@remote: #nonumeros as Mastodon's HTML
	mention(2, 10, time.Minute, 0, false) // to someone else
	exec(`INSERT INTO notifications (account_id, from_account_id, type, created_at, filtered) VALUES
		(1, 2, 'reblog', $1, false), (1, 2, 'reblog', $2, false), (1, 10, 'favourite', $1, false)`,
		at(time.Minute), at(5*24*time.Hour))
	// ids grow with time, as in Mastodon: the 30-day bound relies on it.
	exec(`INSERT INTO favourites (account_id, status_id, created_at) VALUES (1, 7, $3), (1, 6, $2), (1, 5, $1), (2, 8, $1)`,
		at(time.Minute), at(10*24*time.Hour), at(40*24*time.Hour))
	exec(`INSERT INTO follows (account_id, target_account_id, created_at) VALUES (10, 1, $1), (2, 1, $2), (1, 2, $1), (11, 1, $3)`,
		at(time.Minute), at(10*24*time.Hour), at(40*24*time.Hour))
	// A followers-only post of ana; fala favs it and also favs a public one.
	priv := post(1, time.Minute, "visibility = 2")
	for _, sid := range []int64{priv, link} {
		var fid int64
		if err := db.pool.QueryRow(ctx, `INSERT INTO favourites (account_id, status_id, created_at) VALUES (10, $1, $2) RETURNING id`, sid, at(time.Minute)).Scan(&fid); err != nil {
			t.Fatal(err)
		}
		exec(`INSERT INTO notifications (account_id, from_account_id, type, created_at, activity_type, activity_id) VALUES (1, 10, 'favourite', $1, 'Favourite', $2)`, at(time.Minute), fid)
	}

	if err := db.schema(ctx); err != nil {
		t.Fatal(err)
	}
	if db.notifExtra == "" {
		t.Error("notifications.filtered not detected")
	}
	// The bound is the first notification of the last 30 days.
	var first int64
	db.read(ctx, func(tx pgx.Tx) error {
		var err error
		first, err = firstID(ctx, tx, "notifications", now.Add(-30*24*time.Hour))
		return err
	})
	if first != 51 {
		t.Errorf("firstID = %d, want 51", first)
	}

	m, err := db.me(ctx, 1, now, cfg, &bounds{})
	if err != nil {
		t.Fatal(err)
	}
	if m.User != "ana" || m.Posts != 900 || m.Followers != 321 || !m.CanPublic || m.Since.Year() != 2022 {
		t.Errorf("account: %+v", m)
	}
	want := map[string]MeCounts{
		"d": {Posts: 3, PubPosts: 2, BoostsGiven: 1, BoostsGot: 1, FavsGiven: 1, FavsGot: 3, PubFavsGot: 1, Followers: 1, Following: 1, Mentions: 5},
		"w": {Posts: 4, PubPosts: 3, BoostsGiven: 1, BoostsGot: 2, FavsGiven: 1, FavsGot: 3, PubFavsGot: 1, Followers: 1, Following: 1, Mentions: 5},
		"m": {Posts: 5, PubPosts: 4, BoostsGiven: 1, BoostsGot: 2, FavsGiven: 2, FavsGot: 3, PubFavsGot: 1, Followers: 2, Following: 1, Mentions: 8},
	}
	for _, p := range periods {
		if m.Periods[p] != want[p] {
			t.Errorf("period %s: got %+v, want %+v", p, m.Periods[p], want[p])
		}
	}
	got := map[string]string{}
	for _, tk := range m.Talkers {
		got[tk.Acct] = fmt.Sprintf("%d/%d/%d/%d/%v", tk.Counts["d"], tk.Counts["m"], tk.Public["d"], tk.Public["m"], tk.Shown)
	}
	// counts all today/30d, public today/30d, may show
	wantT := map[string]string{"fala@remote.example": "1/2/0/1/true", "muda@remote.example": "1/1/1/1/false",
		"bia": "1/2/0/0/true", "caio": "0/1/0/1/false", "tagi@remote.example": "1/1/1/1/false"}
	if fmt.Sprint(got) != fmt.Sprint(wantT) {
		t.Errorf("talkers (suspended eva out, the filtered one out): %v", got)
	}
	// Public mentions: fala's public one, muda's unlisted one, caio's (suspended eva is out).
	if m.PublicMentions["d"] != 2 || m.PublicMentions["m"] != 4 { // + tagi's public one
		t.Errorf("public mentions: %v", m.PublicMentions)
	}
	var links []string
	for _, l := range m.Links {
		links = append(links, fmt.Sprintf("%s:%v", l.Domain, l.Public))
	}
	if strings.Join(links, " ") != "news.example:true cw.example:false" {
		t.Errorf("links (no DM, the CW one private): %v", links)
	}
	if _, err := db.me(ctx, 4, now, cfg, &bounds{}); err != errNoAccount {
		t.Errorf("account without a user: %v", err)
	}
	if _, err := db.me(ctx, 6, now, cfg, &bounds{}); err != errNoAccount {
		t.Errorf("suspended account: %v", err)
	}
	if _, err := db.me(ctx, 10, now, cfg, &bounds{}); err != errNoAccount {
		t.Errorf("remote account: %v", err)
	}
	exec(`UPDATE accounts SET note = 'tchau #NoNumeros' WHERE id = 1`)
	if m, _ := db.me(ctx, 1, now, cfg, &bounds{}); m == nil || m.CanPublic {
		t.Error("#nonumeros should take the public page away")
	}
}
