package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

func TestStreaks(t *testing.T) {
	today := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	day := func(ago int) string { return today.AddDate(0, 0, -ago).Format("2006-01-02") }
	set := func(ago ...int) map[string]bool {
		m := map[string]bool{}
		for _, a := range ago {
			m[day(a)] = true
		}
		return m
	}
	for _, c := range []struct {
		days          map[string]bool
		current, best int
	}{
		{set(), 0, 0},
		{set(0), 1, 1},
		{set(1, 2), 2, 2}, // nothing yet today: yesterday's streak still counts
		{set(2, 3), 0, 2}, // broken yesterday
		{set(0, 1, 5, 6, 7), 2, 3},
		{set(0, 95, 96, 97), 1, 1}, // outside the window
	} {
		cur, best := streaks(c.days, today, 90)
		if cur != c.current || best != c.best {
			t.Errorf("%v: got %d/%d, want %d/%d", c.days, cur, best, c.current, c.best)
		}
	}
}

func TestTopBars(t *testing.T) {
	got := fmt.Sprint(topBars(map[string]int64{"b": 2, "a": 2, "c": 5, "d": 1}, 3))
	if got != "[{c 5} {a 2} {b 2}]" {
		t.Errorf("topBars: %s", got)
	}
}

func TestMoreQueries(t *testing.T) {
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
	// Noon local time: "an hour ago" is always today.
	l := time.Now().In(cfg.loc)
	now := time.Date(l.Year(), l.Month(), l.Day(), 12, 0, 0, 0, cfg.loc)
	at := func(ago time.Duration) time.Time { return now.Add(-ago).UTC() }
	const day = 24 * time.Hour

	// ana (1) is the account; bia (2) local; fala and quieto remote; eva suspended.
	exec(`INSERT INTO accounts (id, username, domain, discoverable, suspended_at) VALUES
		(1, 'ana', NULL, true, NULL), (2, 'bia', NULL, true, NULL), (6, 'eva', NULL, true, now()),
		(10, 'fala', 'remote.example', true, NULL), (11, 'quieto', 'remote.example', true, NULL), (12, 'novo', 'remote.example', true, NULL)`)
	exec(`INSERT INTO users (account_id) VALUES (1), (2), (6)`)
	exec(`INSERT INTO oauth_applications (id, name) VALUES (1, 'Elk')`)

	n := int64(0)
	post := func(acct int64, ago time.Duration, extra string) int64 {
		t.Helper()
		n++
		id := snowflake(now.Add(-ago)) + n
		exec(`INSERT INTO statuses (id, account_id, created_at) VALUES ($1, $2, $3)`, id, acct, at(ago))
		if extra != "" {
			exec(`UPDATE statuses SET `+extra+` WHERE id = $1`, id)
		}
		return id
	}
	stats := func(sid, favs, boosts, replies int64) {
		exec(`INSERT INTO status_stats (status_id, favourites_count, reblogs_count, replies_count) VALUES ($1, $2, $3, $4)`, sid, favs, boosts, replies)
	}
	media := func(id, sid int64, typ int, alt string, size int) {
		var s any
		if sid != 0 {
			s = sid
		}
		exec(`INSERT INTO media_attachments (id, status_id, account_id, type, file_file_name, description, file_file_size) VALUES ($1, $2, 1, $3, 'f.jpg', $4, $5)`, id, s, typ, alt, size)
	}

	p1 := post(1, time.Minute, "application_id = 1, language = 'pt', text = 'Post um #gatos'")
	p2 := post(1, 2*time.Hour, "visibility = 1, in_reply_to_id = 123, language = 'en', edited_at = now(), text = '<p>resposta</p>'")
	p3 := post(1, 3*day, "visibility = 2, spoiler_text = 'cw text', text = 'escondido'")
	p4 := post(1, time.Hour, "visibility = 3, text = 'segredo'")
	post(1, 5*day, "local_only = true, expires_at = now() + interval '1 day'")
	p6 := post(1, 40*day, "")
	post(1, time.Minute, "deleted_at = now()")
	post(1, day, "")
	boosted := post(2, 2*day, "")
	post(1, time.Minute, fmt.Sprintf("reblog_of_id = %d", boosted)) // ana boosts bia
	stats(p1, 5, 2, 1)
	stats(p2, 1, 0, 4)
	stats(p3, 10, 0, 0)
	stats(p4, 100, 0, 9) // a direct message is never a best post
	media(1, p1, 0, "um gato", 100)
	media(2, p2, 2, "", 200)
	media(3, p3, 0, "", 300) // behind a warning: counted, not in the grid
	media(4, p4, 0, "", 400) // a DM: counted, not in the grid
	media(5, 0, 0, "", 1000) // uploaded, never posted: still takes space

	// A direct message and a public mention from fala.
	for _, vis := range []int{3, 0} {
		sid := post(10, time.Minute, fmt.Sprintf("visibility = %d", vis))
		var mid int64
		if err := db.pool.QueryRow(ctx, `INSERT INTO mentions (account_id, status_id) VALUES (1, $1) RETURNING id`, sid).Scan(&mid); err != nil {
			t.Fatal(err)
		}
		exec(`INSERT INTO notifications (account_id, from_account_id, type, created_at, activity_type, activity_id) VALUES (1, 10, 'mention', $1, 'Mention', $2)`, at(time.Minute), mid)
	}
	// ana favs two of bia's posts and one of fala's.
	b1, b2, f1 := post(2, 2*day, ""), post(2, 2*day, ""), post(10, 2*day, "")
	exec(`INSERT INTO favourites (account_id, status_id, created_at) VALUES (1, $1, $4), (1, $2, $4), (1, $3, $4)`, b1, b2, f1, at(time.Hour))
	// ana also favs a direct message from quieto: that never shows who she talks to.
	qdm := post(11, 2*day, "visibility = 3")
	exec(`INSERT INTO favourites (account_id, status_id, created_at) VALUES (1, $1, $2)`, qdm, at(time.Hour))
	// A limited post (visibility 4, some forks): counted as direct, not in languages or apps.
	post(1, 6*day, "visibility = 4, language = 'xx', application_id = 1")
	// follows: fala and eva follow ana; ana follows bia, fala and quieto for long, novo since last week.
	exec(`INSERT INTO follows (account_id, target_account_id, created_at) VALUES (10, 1, $1), (6, 1, $1),
		(1, 2, $2), (1, 10, $2), (1, 11, $2), (1, 12, $3)`, at(day), at(60*day), at(5*day))
	// polls: one now, one 40 days ago; ana votes twice on someone's poll.
	exec(`INSERT INTO polls (id, account_id, status_id, votes_count, created_at) VALUES (1, 1, $1, 7, $3), (2, 1, $2, 3, $4), (9, 2, $5, 1, $3)`,
		p1, p6, at(time.Minute), at(40*day), boosted)
	// A poll in a DM with more votes never shows; one behind a warning shows the warning.
	dmPoll := post(1, 50*day, "visibility = 3, text = 'enquete secreta'")
	exec(`INSERT INTO polls (id, account_id, status_id, votes_count, created_at) VALUES (3, 1, $1, 50, $2)`, dmPoll, at(50*day))
	exec(`INSERT INTO poll_votes (account_id, poll_id, choice) VALUES (1, 9, 0), (1, 9, 1)`)
	exec(`INSERT INTO tags (id, name, display_name) VALUES (1, 'gatos', 'Gatos'), (2, 'segredo', '')`)
	exec(`INSERT INTO statuses_tags VALUES ($1, 1), ($2, 1), ($3, 2), ($4, 3)`, p1, p2, p4, p3) // p3 is behind a warning
	exec(`INSERT INTO tags (id, name) VALUES (3, 'atrasdoaviso')`)
	exec(`INSERT INTO bookmarks (account_id, status_id, created_at) VALUES (1, $1, $3), (1, $2, $4)`, f1, b1, at(day), at(40*day))

	if err := db.schema(ctx); err != nil {
		t.Fatal(err)
	}
	m, err := db.more(ctx, 1, now, cfg, &bounds{})
	if err != nil {
		t.Fatal(err)
	}
	check := func(what string, got, want any) {
		t.Helper()
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("%s: got %v, want %v", what, got, want)
		}
	}
	check("media", m.Media, map[string]int64{"image": 3, "video": 1})
	check("alt", fmt.Sprint(m.AltDone, "/", m.AltOf), "1/4")
	check("space", m.MediaBytes, 2000)
	if len(m.Photos) != 1 || !strings.HasSuffix(m.Photos[0].URL, fmt.Sprintf("/@ana/%d", p1)) || m.Photos[0].Alt != "um gato" {
		t.Errorf("photos (no CW, no DM): %+v", m.Photos)
	}
	var top []string
	for _, p := range m.Top {
		top = append(top, fmt.Sprintf("%s:%s:%v", p.Text, p.Vis, p.CW))
	}
	check("top", strings.Join(top, " | "), "cw text:private:true | Post um #gatos:public:false | resposta:unlisted:false")
	if m.Replied == nil || m.Replied.ID != p2 || m.Replied.Replies != 4 {
		t.Errorf("most replied: %+v", m.Replied)
	}
	check("visibility", m.Vis, map[string]int64{"direct": 2, "local": 1, "private": 1, "public": 2, "unlisted": 1})
	check("style", fmt.Sprint(m.CW, m.Expiring, m.Edited, m.Replies, m.New, m.DMsGot), "1 1 1 1 4 1")
	check("langs", m.Langs, []Bar{{"?", 3}, {"en", 1}, {"pt", 1}})
	check("apps", m.Apps, []Bar{{"Elk", 1}})
	lt := now.Add(-time.Minute)
	if m.Heat[lt.Weekday()][lt.Hour()] < 1 {
		t.Errorf("heat: nothing at %s %dh", lt.Weekday(), lt.Hour())
	}
	check("streaks", fmt.Sprint(m.Streak, m.BestStreak, m.ActiveDays), "2 2 7")
	acct := func(ps []Person, values bool) string {
		var out []string
		for _, p := range ps {
			if values {
				out = append(out, fmt.Sprintf("%s:%d", p.Acct, p.Value))
			} else {
				out = append(out, p.Acct)
			}
		}
		return strings.Join(out, " ")
	}
	check("fav to", acct(m.FavTo, true), "bia:2 fala@remote.example:1")
	check("boost to", acct(m.BoostTo, true), "bia:1")
	check("new followers (eva suspended)", acct(m.NewFollowers, false), "fala@remote.example")
	check("quiet (fala talks, novo is recent)", acct(m.Quiet, false)+fmt.Sprint(" ", m.QuietN), "bia quieto@remote.example 2")
	check("polls (the DM one counted, it is theirs)", fmt.Sprint(m.Polls, m.PollVotes), "3 60")
	if m.TopPoll == nil || m.TopPoll.Votes != 7 || m.TopPoll.Text != "Post um #gatos" || m.TopPollCW {
		t.Errorf("top poll (not the DM one): %+v", m.TopPoll)
	}
	check("tags (not from the DM)", m.Tags, []Bar{{"Gatos", 2}})
	check("bookmarks", fmt.Sprint(m.Bookmarks, m.BookmarksMonth), "2 1")
	check("bookmark servers", m.BookmarkServers, []Bar{{"inst.example", 1}, {"remote.example", 1}})
	check("votes given", m.VotesGiven, 1)

	exec(`UPDATE statuses SET spoiler_text = 'aviso' WHERE id = $1`, p1)
	if m, _ := db.more(ctx, 1, now, cfg, &bounds{}); m == nil || m.TopPoll == nil || m.TopPoll.Text != "aviso" || !m.TopPollCW {
		t.Errorf("top poll behind a warning: %+v", m)
	}

	// Without the 0.5.0 grants: those parts are empty, the rest still there.
	exec(`DROP TABLE bookmarks; DROP TABLE status_stats; DROP TABLE statuses_tags; DROP TABLE poll_votes`)
	m, err = db.more(ctx, 1, now, cfg, &bounds{})
	if err != nil {
		t.Fatal(err)
	}
	if m.Bookmarks != 0 || len(m.Top) != 0 || len(m.Tags) != 0 || m.VotesGiven != 0 || m.Media["image"] != 3 {
		t.Errorf("without grants: %+v", m)
	}
	if _, err := db.more(ctx, 6, now, cfg, &bounds{}); err != errNoAccount {
		t.Errorf("suspended: %v", err)
	}
	if _, err := db.more(ctx, 10, now, cfg, &bounds{}); err != errNoAccount {
		t.Errorf("remote: %v", err)
	}
}

// TestReadsAsTheAppUser runs every query as the numeros user, after
// docs/database.sql was applied to the test database (and the other database
// tests filled it): the column grants must be enough. Set NUMEROS_TEST_APP_PASSWORD
// to the password given to the script.
func TestReadsAsTheAppUser(t *testing.T) {
	host, pw := os.Getenv("NUMEROS_TEST_DB_HOST"), os.Getenv("NUMEROS_TEST_APP_PASSWORD")
	if host == "" || pw == "" {
		t.Skip("NUMEROS_TEST_DB_HOST or NUMEROS_TEST_APP_PASSWORD not set")
	}
	cfg := testConfig()
	cfg.dbHost, cfg.dbPort = host, env("NUMEROS_TEST_DB_PORT", "5432")
	cfg.dbName, cfg.dbUser, cfg.dbPassword, cfg.dbSSLMode = env("NUMEROS_TEST_DB_NAME", "numeros_test"), "numeros", pw, "disable"
	ctx := context.Background()
	db, err := openStore(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer db.pool.Close()
	if err := db.schema(ctx); err != nil {
		t.Fatal(err)
	}
	col := &collector{cfg: cfg, db: db, client: http.DefaultClient, now: time.Now}
	col.round(ctx)
	if s := col.snapshot(); s == nil || len(s.Stale) > 0 {
		t.Errorf("round as numeros, stale blocks: %v", s.Stale)
	}
	moreWarned.Range(func(k, _ any) bool { moreWarned.Delete(k); return true })
	var ids []int64
	rows, err := db.pool.Query(ctx, `SELECT id FROM accounts WHERE domain IS NULL`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id int64
		rows.Scan(&id)
		ids = append(ids, id)
	}
	rows.Close()
	for _, id := range ids {
		if _, err := db.me(ctx, id, time.Now(), cfg, &bounds{}); err != nil && err != errNoAccount {
			t.Errorf("me(%d) as numeros: %v", id, err)
		}
		if _, err := db.more(ctx, id, time.Now(), cfg, &bounds{}); err != nil && err != errNoAccount {
			t.Errorf("more(%d) as numeros: %v", id, err)
		}
	}
	moreWarned.Range(func(k, _ any) bool {
		t.Errorf("as numeros, left out: %v", k)
		return true
	})
}
