package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestHelpers(t *testing.T) {
	if got := idPartition(108336787171335042); got != "108/336/787/171/335/042" {
		t.Errorf("idPartition: %s", got)
	}
	if got := idPartition(42); got != "000/000/042" {
		t.Errorf("idPartition short: %s", got)
	}
	if got := avatarURL("https://cache.example", "https://inst.example", 108336787171335042, "a.png", false); got != "https://cache.example/accounts/avatars/108/336/787/171/335/042/original/a.png" {
		t.Errorf("avatar: %s", got)
	}
	if got := avatarURL("https://cache.example", "https://inst.example", 42, "a.png", true); !strings.Contains(got, "/cache/accounts/avatars/") {
		t.Errorf("remote avatar: %s", got)
	}
	if got := avatarURL("https://cache.example", "https://inst.example", 42, "", false); got != "https://inst.example/avatars/original/missing.png" {
		t.Errorf("missing avatar: %s", got)
	}
	for in, want := range map[int64]string{0: "0", 999: "999", 8412: "8.412", 2310000: "2.310.000"} {
		if got := formatInt(in, "pt"); got != want {
			t.Errorf("formatInt(%d) = %s", in, got)
		}
	}
	if got := formatInt(8412, "en"); got != "8,412" {
		t.Errorf("formatInt en: %s", got)
	}
	if got := formatBytes(91482624000, "pt"); got != "85,2 GB" {
		t.Errorf("formatBytes: %s", got)
	}
	if got := formatBytes(512, "en"); got != "512 B" {
		t.Errorf("formatBytes small: %s", got)
	}
	now := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	if got := age(time.Date(2022, 5, 22, 0, 0, 0, 0, time.UTC), now, "pt"); got != "há 4 anos e 5 meses" {
		t.Errorf("age: %s", got)
	}
	if got := age(time.Date(2025, 11, 1, 0, 0, 0, 0, time.UTC), now, "en"); got != "1 year ago" {
		t.Errorf("age en: %s", got)
	}
	if got := clip("um dois três quatro cinco", 12); got != "um dois…" {
		t.Errorf("clip: %q", got)
	}
	if got := stripTags(`<p>Café &amp; pão<br>ok</p>`); got != "Café & pão ok" {
		t.Errorf("stripTags: %q", got)
	}
	if got := nextRound(time.Date(2026, 1, 1, 10, 3, 0, 0, time.UTC), time.Hour, 7*time.Minute); !got.Equal(time.Date(2026, 1, 1, 10, 7, 0, 0, time.UTC)) {
		t.Errorf("nextRound: %v", got)
	}
	if got := nextRound(time.Date(2026, 1, 1, 10, 7, 0, 0, time.UTC), time.Hour, 7*time.Minute); !got.Equal(time.Date(2026, 1, 1, 11, 7, 0, 0, time.UTC)) {
		t.Errorf("nextRound on the minute: %v", got)
	}
	loc, _ := time.LoadLocation("America/Sao_Paulo")
	if got := periodStart("d", time.Date(2026, 1, 1, 2, 0, 0, 0, time.UTC), loc); !got.Equal(time.Date(2025, 12, 31, 0, 0, 0, 0, loc)) {
		t.Errorf("today starts at local midnight: %v", got)
	}
	if snowflake(time.UnixMilli(1)) != 1<<16 {
		t.Error("snowflake")
	}
}

func testConfig() config {
	loc, _ := time.LoadLocation("America/Sao_Paulo")
	return config{instance: "https://inst.example", media: "https://cache.example", loc: loc, every: time.Hour, offset: 7 * time.Minute}
}

func testSnapshot(now time.Time) *Snapshot {
	people := []Person{{Acct: "ana", Name: "Ana <Lima>", URL: "https://inst.example/@ana", Avatar: "https://cache.example/a.png", Value: 40, Total: 900}}
	r := func(k string, tot bool) Ranking {
		return Ranking{Key: k, Totals: tot, Periods: map[string][]Person{"d": people, "w": people, "m": nil}}
	}
	return &Snapshot{
		At: now.Add(-10 * time.Minute), Next: now.Add(50 * time.Minute), Users: 2301, Active: 900, Posts: 2310000, Servers: 42818,
		Since:    time.Date(2022, 5, 22, 15, 0, 0, 0, time.UTC),
		Rankings: []Ranking{r("toots", false), r("followers", true)},
		Links:    []Link{{URL: "https://news.example/a", Title: "Notícia", Domain: "news.example", Acct: "ana", At: now.Add(-time.Hour)}},
		Photos:   []Photo{{Src: "https://cache.example/p.png", Alt: "", URL: "https://inst.example/@ana/1", Acct: "ana"}},
		Polls:    []Poll{{Text: "Café?", Acct: "ana", Votes: 1, URL: "https://inst.example/@ana/2"}},
		NewUsers: Windows{1, 2, 3, 4}, Reports: Windows{5, 6, 7, 8}, ReportsTotal: 196,
		Clients:   []Bar{{"Web", 10}, {"Ivory", 5}},
		Languages: []Bar{{"pt", 10}, {"xx", 1}},
		Talkers:   []Bar{{"mastodon.social", 9}},
		Software:  []Item{{Key: "mastodon", Value: "4.7.5"}},
		Space:     []Item{{Key: "postgresql", Bytes: 91482624000}, {Key: "media", Bytes: 1 << 40}},
	}
}

func TestPage(t *testing.T) {
	cfg := testConfig()
	col := &collector{cfg: cfg, now: time.Now}
	srv, err := newServer(cfg, col)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.routes())
	defer ts.Close()
	get := func(path string) (*http.Response, string) {
		res, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(res.Body)
		res.Body.Close()
		return res, string(b)
	}
	// Before the first round.
	if _, body := get("/"); !strings.Contains(body, "Coletando") {
		t.Error("no collecting notice")
	}
	if res, _ := get("/numeros.json"); res.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("json before data: %d", res.StatusCode)
	}

	col.last = testSnapshot(time.Now())
	res, body := get("/")
	csp := res.Header.Get("Content-Security-Policy")
	if !strings.Contains(csp, "img-src 'self' https://cache.example https://inst.example") || strings.Contains(csp, "unsafe") {
		t.Errorf("csp: %s", csp)
	}
	for _, want := range []string{"Ana &lt;Lima&gt;", "@ana", "900 no total", "2.301", "2,31 mi", "22 mai 2022", "há 4 anos",
		`id="p-d" checked`, "Notícia", "news.example", "Foto de @ana", "1 voto", "196 denúncias", "Português", "XX",
		"mastodon.social", "4.7.5", "85,2 GB", "1,0 TB", "Ninguém por aqui ainda", `href="/en/"`, "#nonumeros"} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	if p := os.Getenv("NUMEROS_DUMP"); p != "" { // a look at the page: NUMEROS_DUMP=dir go test -run TestPage
		os.WriteFile(p+"/index.html", []byte(strings.Replace(body, `href="/numeros.css"`, `href="numeros.css"`, 1)), 0o600)
		os.WriteFile(p+"/numeros.css", srv.css, 0o600)
	}
	if strings.Contains(body, "style=") || strings.Contains(body, "<script") {
		t.Error("inline style or script on the page: the CSP would block it")
	}
	if _, body := get("/en/"); !strings.Contains(body, "Most toots") || !strings.Contains(body, "May 22, 2022") || !strings.Contains(body, "2.31M") {
		t.Error("english page")
	}
	res, body = get("/numeros.json")
	var s Snapshot
	if res.StatusCode != 200 || json.Unmarshal([]byte(body), &s) != nil || s.Users != 2301 {
		t.Errorf("json: %d", res.StatusCode)
	}
	if res, body := get("/release-notes"); res.StatusCode != 200 || !strings.Contains(body, releases[0].Version) {
		t.Errorf("release notes: %d", res.StatusCode)
	}
	if res, _ := get("/nope"); res.StatusCode != 404 {
		t.Errorf("unknown path: %d", res.StatusCode)
	}
}

// --- against a database with the parts of Mastodon's schema numeros reads ---

const testSchema = `
DROP SCHEMA IF EXISTS public CASCADE; CREATE SCHEMA public;
CREATE TABLE accounts (id bigint PRIMARY KEY, username text NOT NULL, domain text, display_name text DEFAULT '',
  avatar_file_name text, url text, uri text DEFAULT '', discoverable boolean, suspended_at timestamp, silenced_at timestamp,
  moved_to_account_id bigint, memorial boolean DEFAULT false, actor_type text, note text DEFAULT '', hide_collections boolean);
CREATE TABLE users (id bigserial PRIMARY KEY, account_id bigint, approved boolean DEFAULT true, confirmed_at timestamp DEFAULT now(),
  disabled boolean DEFAULT false, created_at timestamp DEFAULT now(), current_sign_in_at timestamp);
CREATE TABLE account_stats (account_id bigint PRIMARY KEY, statuses_count bigint DEFAULT 0, followers_count bigint DEFAULT 0, following_count bigint DEFAULT 0);
CREATE TABLE statuses (id bigint PRIMARY KEY, account_id bigint, text text DEFAULT '', created_at timestamp DEFAULT now(),
  visibility int DEFAULT 0, reblog_of_id bigint, deleted_at timestamp, sensitive boolean DEFAULT false, spoiler_text text DEFAULT '',
  language text, application_id bigint, poll_id bigint, local_only boolean, expires_at timestamp);
CREATE TABLE notifications (id bigserial PRIMARY KEY, account_id bigint, from_account_id bigint, type text, created_at timestamp DEFAULT now());
CREATE TABLE favourites (id bigserial PRIMARY KEY, account_id bigint, status_id bigint, created_at timestamp DEFAULT now());
CREATE TABLE follows (id bigserial PRIMARY KEY, account_id bigint, target_account_id bigint, created_at timestamp DEFAULT now());
CREATE TABLE preview_cards (id bigserial PRIMARY KEY, url text, title text);
CREATE TABLE preview_cards_statuses (preview_card_id bigint, status_id bigint);
CREATE TABLE media_attachments (id bigint PRIMARY KEY, status_id bigint, type int, file_file_name text, description text);
CREATE TABLE polls (id bigint PRIMARY KEY, votes_count bigint);
CREATE TABLE reports (id bigserial PRIMARY KEY, created_at timestamp DEFAULT now());
CREATE TABLE oauth_applications (id bigint PRIMARY KEY, name text);
CREATE TABLE instances (domain text);
`

func TestCollectKeepsToThePrivacyRules(t *testing.T) {
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

	now := time.Now()
	sid := func(ago time.Duration, n int64) int64 { return snowflake(now.Add(-ago)) + n }
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := db.pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	// Local people: ana is fine; bia isn't discoverable; caio opted out; duda hides
	// her graph; robo is a bot; eva is suspended.
	for _, a := range []struct {
		id               int64
		user, note       string
		disc, hide       bool
		actor, suspended string
	}{
		{1, "ana", "", true, false, "Person", ""}, {2, "bia", "", false, false, "Person", ""},
		{3, "caio", "não me conte #NoNumeros", true, false, "Person", ""}, {4, "duda", "", true, true, "Person", ""},
		{5, "robo", "", true, false, "Service", ""}, {6, "eva", "", true, false, "Person", "now"},
	} {
		var susp any
		if a.suspended != "" {
			susp = now.UTC()
		}
		exec(`INSERT INTO accounts (id, username, avatar_file_name, discoverable, hide_collections, actor_type, note, suspended_at)
			VALUES ($1, $2, 'av.png', $3, $4, $5, $6, $7)`, a.id, a.user, a.disc, a.hide, a.actor, a.note, susp)
		exec(`INSERT INTO users (account_id, created_at, current_sign_in_at) VALUES ($1, now() - interval '3 days', now())`, a.id)
		exec(`INSERT INTO account_stats (account_id, statuses_count, followers_count) VALUES ($1, 10, 100)`, a.id)
	}
	// Remote: fala@remote.example is fine, muda@remote.example isn't discoverable.
	exec(`INSERT INTO accounts (id, username, domain, discoverable, url) VALUES
		(10, 'fala', 'remote.example', true, 'https://remote.example/@fala'), (11, 'muda', 'remote.example', false, '')`)
	exec(`INSERT INTO instances VALUES ('remote.example'), ('other.example')`)

	// Everyone local posts; the counts must only show ana, duda.
	n := int64(0)
	post := func(acct int64, extra string) int64 {
		n++
		id := sid(time.Hour, n)
		exec(`INSERT INTO statuses (id, account_id, created_at, language, application_id) VALUES ($1, $2, now() - interval '1 hour', 'pt', 100)`, id, acct)
		if extra != "" {
			exec(`UPDATE statuses SET `+extra+` WHERE id = $1`, id)
		}
		return id
	}
	for _, acct := range []int64{1, 1, 1, 2, 2, 2, 2, 3, 3, 3, 3, 4, 5, 5, 5, 5, 5, 6, 6, 6, 6} {
		post(acct, "")
	}
	exec(`INSERT INTO oauth_applications VALUES (100, 'Web'), (101, 'MeuScript')`)
	post(1, "application_id = 101") // only ana uses MeuScript
	// Each install of a mobile app is its own OAuth application: Ivory counts by name.
	exec(`INSERT INTO oauth_applications VALUES (102, 'Ivory'), (103, 'Ivory'), (104, 'Ivory')`)
	post(1, "application_id = 102")
	post(4, "application_id = 103")
	post(2, "application_id = 104")

	// Content: a link, a photo and a poll by ana; the same kind of thing behind
	// a content warning, sensitive, local-only or by bia must not show.
	link := post(1, "text = 'olha'")
	exec(`INSERT INTO preview_cards (id, url, title) VALUES (1, 'https://www.news.example/x', 'Notícia boa'), (2, 'https://hidden.example/y', 'Escondida')`)
	exec(`INSERT INTO preview_cards_statuses VALUES (1, $1)`, link)
	for _, extra := range []string{"spoiler_text = 'cw'", "local_only = true", "visibility = 1", "expires_at = now() + interval '1 day'"} {
		exec(`INSERT INTO preview_cards_statuses VALUES (2, $1)`, post(1, extra))
	}
	exec(`INSERT INTO preview_cards_statuses VALUES (2, $1)`, post(2, ""))
	photo := post(1, "")
	exec(`INSERT INTO media_attachments VALUES (500, $1, 0, 'foto.png', 'um gato')`, photo)
	exec(`INSERT INTO media_attachments VALUES (501, $1, 0, 'nsfw.png', '')`, post(1, "sensitive = true"))
	exec(`INSERT INTO polls VALUES (7, 12)`)
	post(1, "poll_id = 7, text = 'Café ou chá?'")
	exec(`INSERT INTO polls VALUES (8, 3)`)
	post(2, "poll_id = 8, text = 'Escondida?'")

	// Interactions.
	exec(`INSERT INTO notifications (account_id, from_account_id, type) VALUES
		(1, 10, 'mention'), (1, 10, 'mention'), (1, 11, 'mention'), (1, 11, 'mention'), (1, 11, 'mention'),
		(1, 10, 'reblog'), (2, 10, 'reblog'), (2, 10, 'reblog')`)
	exec(`INSERT INTO favourites (account_id, status_id) VALUES (10, $1), (1, $1), (10, $2)`, link, photo) // ana's own fav doesn't count
	exec(`INSERT INTO follows (account_id, target_account_id) VALUES (10, 1), (10, 4), (11, 4), (1, 4)`)
	exec(`INSERT INTO reports (created_at) VALUES (now() - interval '3 days'), (now() - interval '100 days')`)

	if err := db.schema(ctx); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(db.postExtra, "local_only") || !strings.Contains(db.postExtra, "expires_at") {
		t.Errorf("optional columns: %q", db.postExtra)
	}
	col := &collector{cfg: cfg, db: db, client: http.DefaultClient, now: time.Now}
	col.round(ctx)
	s := col.snapshot()
	if len(s.Stale) > 0 {
		t.Fatalf("stale blocks: %v", s.Stale)
	}
	names := func(key, period string) string {
		for _, r := range s.Rankings {
			if r.Key == key {
				var out []string
				for _, p := range r.Periods[period] {
					out = append(out, fmt.Sprintf("%s=%d", p.Acct, p.Value))
				}
				return strings.Join(out, " ")
			}
		}
		return "missing"
	}
	for key, want := range map[string]string{
		"toots":     "ana=13 duda=2", // bia, caio, robo and eva never show up
		"boosts":    "ana=1",
		"favs":      "ana=2",
		"followers": "ana=1", // duda hides her graph
		"following": "ana=1",
		"external":  "fala@remote.example=2",
	} {
		if got := names(key, "w"); got != want {
			t.Errorf("%s: %q, want %q", key, got, want)
		}
	}
	if got := names("toots", "d"); got != "ana=13 duda=2" && now.In(cfg.loc).Hour() >= 1 {
		t.Errorf("toots today: %q", got)
	}
	if len(s.Links) != 1 || s.Links[0].Domain != "news.example" || s.Links[0].Acct != "ana" {
		t.Errorf("links: %+v", s.Links)
	}
	if len(s.Photos) != 1 || s.Photos[0].Alt != "um gato" || !strings.HasSuffix(s.Photos[0].Src, "/000/000/500/small/foto.png") {
		t.Errorf("photos: %+v", s.Photos)
	}
	if len(s.Polls) != 1 || s.Polls[0].Text != "Café ou chá?" || s.Polls[0].Votes != 12 {
		t.Errorf("polls: %+v", s.Polls)
	}
	if s.Users != 5 || s.Servers != 2 || s.Active != 6 || s.Posts != 60 {
		t.Errorf("server: users %d servers %d active %d posts %d", s.Users, s.Servers, s.Active, s.Posts)
	}
	if s.NewUsers != (Windows{6, 6, 6, 6}) || s.Reports != (Windows{1, 1, 2, 2}) || s.ReportsTotal != 2 {
		t.Errorf("windows: %+v %+v %d", s.NewUsers, s.Reports, s.ReportsTotal)
	}
	if len(s.Clients) != 2 || s.Clients[0].Name != "Web" || s.Clients[1] != (Bar{"Ivory", 3}) {
		t.Errorf("clients (MeuScript has one person): %+v", s.Clients)
	}
	if len(s.Languages) != 1 || s.Languages[0].Name != "pt" {
		t.Errorf("languages: %+v", s.Languages)
	}
	if len(s.Talkers) != 1 || s.Talkers[0] != (Bar{"remote.example", 5}) {
		t.Errorf("talkers: %+v", s.Talkers)
	}
	if len(s.Software) != 1 || s.Software[0].Key != "postgresql" || len(s.Space) != 1 {
		t.Errorf("without the admin API, the database's own version and size: %+v %+v", s.Software, s.Space)
	}
}
