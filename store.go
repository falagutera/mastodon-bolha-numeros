package main

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Who may show up on the page. A local account must be discoverable, in good
// standing (not suspended, silenced, moved, a memorial or a bot) and a real
// confirmed user; #nonumeros in the bio takes anyone out.
const localOK = `a.domain IS NULL AND a.discoverable IS TRUE
	AND a.suspended_at IS NULL AND a.silenced_at IS NULL AND a.moved_to_account_id IS NULL
	AND a.memorial IS NOT TRUE AND COALESCE(a.actor_type, 'Person') NOT IN ('Service', 'Application')
	AND COALESCE(a.note, '') NOT ILIKE '%#nonumeros%'
	AND EXISTS (SELECT 1 FROM users u WHERE u.account_id = a.id
		AND u.approved AND u.confirmed_at IS NOT NULL AND NOT u.disabled)`

// remoteOK is the same rule for accounts from other servers.
const remoteOK = `a.domain IS NOT NULL AND a.discoverable IS TRUE
	AND a.suspended_at IS NULL AND a.silenced_at IS NULL AND a.moved_to_account_id IS NULL
	AND a.memorial IS NOT TRUE AND COALESCE(a.actor_type, 'Person') NOT IN ('Service', 'Application')
	AND COALESCE(a.note, '') NOT ILIKE '%#nonumeros%'`

// A post can be shown when it is public, not a boost, has no content warning
// and no sensitive media.
const postOK = `s.visibility = 0 AND s.reblog_of_id IS NULL AND s.deleted_at IS NULL
	AND s.sensitive IS NOT TRUE AND COALESCE(s.spoiler_text, '') = ''`

type store struct {
	pool *pgxpool.Pool
	// postExtra adds the filters for columns only some Mastodons have
	// (local-only and expiring posts, from the bolha.us build).
	postExtra string
}

func openStore(ctx context.Context, c config) (*store, error) {
	u := url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(c.dbUser, c.dbPassword),
		Host:     net.JoinHostPort(c.dbHost, c.dbPort),
		Path:     "/" + c.dbName,
		RawQuery: url.Values{"sslmode": {c.dbSSLMode}}.Encode(),
	}
	cfg, err := pgxpool.ParseConfig(u.String())
	if err != nil {
		return nil, fmt.Errorf("database settings: %w", err)
	}
	// One query at a time: the collector is the only user, and the database is shared.
	cfg.MaxConns = 1
	cfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("database: %w", err)
	}
	return &store{pool: pool}, nil
}

// schema looks for the optional columns once.
func (s *store) schema(ctx context.Context) error {
	rows, err := s.pool.Query(ctx, `SELECT column_name FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = 'statuses' AND column_name IN ('local_only', 'expires_at')`)
	if err != nil {
		return fmt.Errorf("reading the schema: %w", err)
	}
	defer rows.Close()
	var extra []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return err
		}
		switch c {
		case "local_only":
			extra = append(extra, "s.local_only IS NOT TRUE")
		case "expires_at":
			extra = append(extra, "s.expires_at IS NULL")
		}
	}
	sort.Strings(extra)
	if len(extra) > 0 {
		s.postExtra = " AND " + strings.Join(extra, " AND ")
	}
	return rows.Err()
}

// read runs fn in a read-only transaction with a time limit.
func (s *store) read(ctx context.Context, fn func(pgx.Tx) error) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, "SET LOCAL statement_timeout = '60s'"); err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// rankingSQL: how many each account got, per key. $1 is a status id
// (snowflake) or a timestamp, as each key needs.
var rankingSQL = map[string]struct {
	counts string
	remote bool
	byID   bool // $1 is a snowflake id, not a timestamp
	total  string
	hidden bool // leave out who hides their social graph
}{
	"toots": {counts: `SELECT a.id AS account_id, (SELECT count(*) FROM statuses s WHERE s.account_id = a.id
		AND s.id >= $1 AND s.reblog_of_id IS NULL AND s.deleted_at IS NULL AND s.visibility < 3) AS n
		FROM accounts a WHERE a.domain IS NULL`, byID: true},
	"boosts": {counts: `SELECT account_id, count(*) AS n FROM notifications
		WHERE type = 'reblog' AND created_at >= $1 GROUP BY 1`},
	"favs": {counts: `SELECT s.account_id, count(*) AS n FROM favourites f JOIN statuses s ON s.id = f.status_id
		WHERE f.created_at >= $1 AND f.account_id <> s.account_id GROUP BY 1`},
	"followers": {counts: `SELECT target_account_id AS account_id, count(*) AS n FROM follows
		WHERE created_at >= $1 GROUP BY 1`, total: "followers_count", hidden: true},
	"following": {counts: `SELECT account_id, count(*) AS n FROM follows
		WHERE created_at >= $1 GROUP BY 1`, total: "following_count", hidden: true},
	"external": {counts: `SELECT from_account_id AS account_id, count(*) AS n FROM notifications
		WHERE type = 'mention' AND created_at >= $1 GROUP BY 1`, remote: true},
}

var rankingOrder = []string{"toots", "boosts", "favs", "followers", "following", "external"}

func (s *store) ranking(ctx context.Context, tx pgx.Tx, key string, since time.Time, c config) ([]Person, error) {
	q := rankingSQL[key]
	filter := localOK
	if q.remote {
		filter = remoteOK
	}
	if q.hidden {
		filter += " AND a.hide_collections IS NOT TRUE"
	}
	total := "0"
	if q.total != "" {
		total = "COALESCE(st." + q.total + ", 0)"
	}
	sql := `WITH c AS (` + q.counts + `)
		SELECT a.id, a.username, COALESCE(a.domain, ''), COALESCE(a.display_name, ''), COALESCE(a.avatar_file_name, ''),
			COALESCE(a.url, a.uri, ''), c.n, ` + total + `
		FROM c JOIN accounts a ON a.id = c.account_id LEFT JOIN account_stats st ON st.account_id = a.id
		WHERE c.n > 0 AND ` + filter + `
		ORDER BY c.n DESC, a.id LIMIT 12`
	var arg any = since.UTC()
	if q.byID {
		arg = snowflake(since)
	}
	rows, err := tx.Query(ctx, sql, arg)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Person{}
	for rows.Next() {
		var (
			id             int64
			user, dom      string
			name, ava, url string
			p              Person
		)
		if err := rows.Scan(&id, &user, &dom, &name, &ava, &url, &p.Value, &p.Total); err != nil {
			return nil, err
		}
		p.Acct, p.Name = user, strings.TrimSpace(name)
		if p.Name == "" {
			p.Name = user
		}
		if dom != "" {
			p.Acct = user + "@" + dom
		}
		p.URL = url
		if dom == "" || p.URL == "" {
			p.URL = c.instance + "/@" + user
		}
		p.Avatar = avatarURL(c.media, c.instance, id, ava, dom != "")
		out = append(out, p)
	}
	return out, rows.Err()
}

// recentSQL: newest public posts of each eligible account, a few per account.
func (s *store) recentSQL(extraWhere string, perAccount, limit int) string {
	return fmt.Sprintf(`SELECT s.id, a.username, s.created_at, COALESCE(s.text, '') AS text FROM accounts a
		CROSS JOIN LATERAL (SELECT s.id, s.created_at, s.text FROM statuses s WHERE s.account_id = a.id AND s.id >= $1
			AND %s%s%s ORDER BY s.id DESC LIMIT %d) s
		WHERE %s ORDER BY s.id DESC LIMIT %d`, postOK, s.postExtra, extraWhere, perAccount, localOK, limit)
}

func (s *store) content(ctx context.Context, tx pgx.Tx, now time.Time, c config) (links []Link, photos []Photo, polls []Poll, err error) {
	week := snowflake(now.Add(-7 * 24 * time.Hour))
	recent := s.recentSQL("", 20, 400)

	rows, err := tx.Query(ctx, `WITH r AS (`+recent+`)
		SELECT pc.url, COALESCE(pc.title, ''), r.username, r.created_at, r.id FROM r
		JOIN preview_cards_statuses pcs ON pcs.status_id = r.id JOIN preview_cards pc ON pc.id = pcs.preview_card_id
		WHERE COALESCE(pc.title, '') <> '' ORDER BY r.id DESC LIMIT 60`, week)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("links: %w", err)
	}
	seen := map[string]bool{}
	for rows.Next() {
		var l Link
		var id int64
		if err := rows.Scan(&l.URL, &l.Title, &l.Acct, &l.At, &id); err != nil {
			rows.Close()
			return nil, nil, nil, err
		}
		u, perr := url.Parse(l.URL)
		if perr != nil || (u.Scheme != "https" && u.Scheme != "http") || seen[l.URL] || len(links) == 12 {
			continue
		}
		seen[l.URL] = true
		l.Domain = strings.TrimPrefix(u.Hostname(), "www.")
		l.Title = clip(l.Title, 110)
		links = append(links, l)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, nil, err
	}

	rows, err = tx.Query(ctx, `WITH r AS (`+recent+`)
		SELECT m.id, m.file_file_name, COALESCE(m.description, ''), r.username, r.id FROM r
		JOIN media_attachments m ON m.status_id = r.id
		WHERE m.type = 0 AND m.file_file_name IS NOT NULL ORDER BY m.id DESC LIMIT 60`, week)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("photos: %w", err)
	}
	per := map[string]int{}
	for rows.Next() {
		var mid, sid int64
		var file string
		var p Photo
		if err := rows.Scan(&mid, &file, &p.Alt, &p.Acct, &sid); err != nil {
			rows.Close()
			return nil, nil, nil, err
		}
		// At most two per person, so one album doesn't fill the grid.
		if per[p.Acct] == 2 || len(photos) == 12 {
			continue
		}
		per[p.Acct]++
		p.Src = mediaURL(c.media, mid, file, "small")
		p.URL = fmt.Sprintf("%s/@%s/%d", c.instance, p.Acct, sid)
		p.Alt = clip(p.Alt, 300)
		photos = append(photos, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, nil, err
	}

	rows, err = tx.Query(ctx, `WITH r AS (`+s.recentSQL(" AND s.poll_id IS NOT NULL", 3, 12)+`)
		SELECT r.id, r.text, r.username, COALESCE(p.votes_count, 0) FROM r JOIN statuses s ON s.id = r.id
		JOIN polls p ON p.id = s.poll_id ORDER BY r.id DESC`, snowflake(now.Add(-90*24*time.Hour)))
	if err != nil {
		return nil, nil, nil, fmt.Errorf("polls: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var p Poll
		var id int64
		if err := rows.Scan(&id, &p.Text, &p.Acct, &p.Votes); err != nil {
			return nil, nil, nil, err
		}
		p.Text = clip(stripTags(p.Text), 140)
		p.URL = fmt.Sprintf("%s/@%s/%d", c.instance, p.Acct, id)
		polls = append(polls, p)
	}
	return links, photos, polls, rows.Err()
}

// server: the instance-wide numbers.
func (s *store) server(ctx context.Context, tx pgx.Tx, snap *Snapshot) error {
	err := tx.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM users u JOIN accounts a ON a.id = u.account_id
			WHERE u.approved AND u.confirmed_at IS NOT NULL AND NOT u.disabled AND a.suspended_at IS NULL),
		(SELECT COALESCE(sum(st.statuses_count), 0) FROM users u JOIN account_stats st ON st.account_id = u.account_id),
		(SELECT count(*) FROM instances),
		(SELECT COALESCE(min(created_at), now()) FROM users),
		(SELECT count(*) FROM users WHERE current_sign_in_at >= now() - interval '30 days')`).
		Scan(&snap.Users, &snap.Posts, &snap.Servers, &snap.Since, &snap.Active)
	if err != nil {
		return fmt.Errorf("server: %w", err)
	}
	win := func(table, where string, w *Windows) error {
		return tx.QueryRow(ctx, `SELECT
			count(*) FILTER (WHERE created_at >= now() - interval '7 days'),
			count(*) FILTER (WHERE created_at >= now() - interval '30 days'),
			count(*) FILTER (WHERE created_at >= now() - interval '182 days'),
			count(*) FILTER (WHERE created_at >= now() - interval '365 days')
			FROM `+table+` WHERE `+where).Scan(&w.Week, &w.Month, &w.HalfYear, &w.Year)
	}
	if err := win("users", "approved AND confirmed_at IS NOT NULL", &snap.NewUsers); err != nil {
		return fmt.Errorf("new users: %w", err)
	}
	if err := win("reports", "true", &snap.Reports); err != nil {
		return fmt.Errorf("reports: %w", err)
	}
	return tx.QueryRow(ctx, `SELECT count(*) FROM reports`).Scan(&snap.ReportsTotal)
}

// usage: languages and apps of the last 30 days of local posts, and the
// servers whose people mention ours the most.
func (s *store) usage(ctx context.Context, tx pgx.Tx, now time.Time, snap *Snapshot, c config) error {
	since := now.Add(-30 * 24 * time.Hour)
	local := `FROM accounts a CROSS JOIN LATERAL (SELECT s.language, s.application_id, s.account_id FROM statuses s
		WHERE s.account_id = a.id AND s.id >= $1 AND s.reblog_of_id IS NULL AND s.deleted_at IS NULL AND s.visibility < 3) s
		WHERE a.domain IS NULL`
	bars := func(sql string, args ...any) ([]Bar, error) {
		rows, err := tx.Query(ctx, sql, args...)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		out := []Bar{}
		for rows.Next() {
			var b Bar
			if err := rows.Scan(&b.Name, &b.Value); err != nil {
				return nil, err
			}
			out = append(out, b)
		}
		return out, rows.Err()
	}
	var err error
	if snap.Languages, err = bars(`SELECT COALESCE(NULLIF(s.language, ''), '?'), count(*) `+local+`
		GROUP BY 1 ORDER BY 2 DESC LIMIT 7`, snowflake(since)); err != nil {
		return fmt.Errorf("languages: %w", err)
	}
	for i := range snap.Languages {
		snap.Languages[i].Name = strings.ToLower(snap.Languages[i].Name)
	}
	// An app only shows up when at least 3 people used it: a personal app's
	// name would say who posts with it.
	if snap.Clients, err = bars(`SELECT o.name, x.n FROM (SELECT s.application_id, count(*) AS n, count(DISTINCT s.account_id) AS people `+local+`
		AND s.application_id IS NOT NULL GROUP BY 1) x JOIN oauth_applications o ON o.id = x.application_id
		WHERE x.people >= 3 ORDER BY x.n DESC LIMIT 12`, snowflake(since)); err != nil {
		return fmt.Errorf("clients: %w", err)
	}
	if snap.Talkers, err = bars(`SELECT a.domain, count(*) FROM notifications n JOIN accounts a ON a.id = n.from_account_id
		WHERE n.type = 'mention' AND n.created_at >= $1 AND a.domain IS NOT NULL AND a.suspended_at IS NULL
		GROUP BY 1 ORDER BY 2 DESC LIMIT 12`, since.UTC()); err != nil {
		return fmt.Errorf("servers: %w", err)
	}
	return nil
}

// database: the PostgreSQL version and size, used when the admin API doesn't say.
func (s *store) database(ctx context.Context, tx pgx.Tx) (version string, size int64, err error) {
	err = tx.QueryRow(ctx, `SELECT current_setting('server_version'), pg_database_size(current_database())`).Scan(&version, &size)
	if f := strings.Fields(version); len(f) > 0 {
		version = f[0]
	}
	return version, size, err
}

var tagRe = strings.NewReplacer("<br>", " ", "<br/>", " ", "<br />", " ", "</p><p>", " ")

// stripTags drops HTML from a post's text (remote posts are HTML; local ones
// are the source text, where this is a no-op).
func stripTags(s string) string {
	s = tagRe.Replace(s)
	var b strings.Builder
	in := false
	for _, r := range s {
		switch {
		case r == '<':
			in = true
		case r == '>':
			in = false
		case !in:
			b.WriteRune(r)
		}
	}
	return strings.NewReplacer("&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", `"`, "&#39;", "'").Replace(b.String())
}
