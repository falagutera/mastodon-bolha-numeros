package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

// The numbers of one account, for /minhaconta and the public /@user pages.
// Unlike the main page they are computed on a visit, from queries on the
// account's own rows, and kept a few minutes per account.

// MeCounts: what one account did and got in one period.
type MeCounts struct {
	Posts, BoostsGiven, BoostsGot, FavsGiven, FavsGot, Followers, Following, Mentions int64
	// For the public page: only posts others could see without following
	// (public or unlisted, not local-only), and favs received on those.
	PubPosts, PubFavsGot int64
}

// MeTalker: someone who mentioned the account, with the count per period.
type MeTalker struct {
	Person
	Counts map[string]int64 `json:"counts"`
	// Public: only mentions in public or unlisted posts. The public page uses
	// these, so it never tells who sends the person direct or followers-only messages.
	Public map[string]int64 `json:"public"`
	Shown  bool             `json:"shown"` // passes the main page's rules, so it may show on the public page
}

// MeLink: a link the account posted.
type MeLink struct {
	Link
	Public bool `json:"public"` // from a post the main page could show
}

type Me struct {
	At                          time.Time
	ID                          int64
	User, Name, Avatar, URL     string
	Since                       time.Time
	Posts, Followers, Following int64 // all time
	HidesGraph                  bool  // hides who follows them: the public page leaves follower numbers out
	CanPublic                   bool  // may have a public page now (no #nonumeros, not limited or moved)
	Periods                     map[string]MeCounts
	PublicMentions              map[string]int64 // mentions in public or unlisted posts, for the public page
	Talkers                     []MeTalker
	Links                       []MeLink
}

var errNoAccount = errors.New("account not found")

// The account: local, approved and not suspended.
const meAccountSQL = `SELECT a.id, a.username, COALESCE(a.display_name, ''), COALESCE(a.avatar_file_name, ''), a.created_at,
	a.hide_collections IS TRUE, COALESCE(st.statuses_count, 0), COALESCE(st.followers_count, 0), COALESCE(st.following_count, 0),
	(COALESCE(a.note, '') !~* '#(<[^>]*>)*nonumeros' AND a.silenced_at IS NULL AND a.moved_to_account_id IS NULL AND a.memorial IS NOT TRUE)
	FROM accounts a LEFT JOIN account_stats st ON st.account_id = a.id
	WHERE a.id = $1 AND a.domain IS NULL AND a.suspended_at IS NULL
	AND EXISTS (SELECT 1 FROM users u WHERE u.account_id = a.id AND u.approved AND u.confirmed_at IS NOT NULL AND NOT u.disabled)`

// bounds caches, per table, the first id of the last 30 days. notifications and
// favourites have plain sequence ids, so without it a count would read every
// row the account ever had (156k notifications for the busiest account). A
// bound up to an hour old is earlier than the real one, which is still right:
// the queries filter on created_at too.
type bounds struct {
	mu sync.Mutex
	at time.Time
	id map[string]int64
}

func (b *bounds) get(ctx context.Context, tx pgx.Tx, since time.Time, now time.Time) (map[string]int64, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.id != nil && now.Sub(b.at) < time.Hour {
		return b.id, nil
	}
	out := map[string]int64{}
	for _, table := range []string{"notifications", "favourites"} {
		id, err := firstID(ctx, tx, table, since)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", table, err)
		}
		out[table] = id
	}
	b.id, b.at = out, now
	return out, nil
}

// firstID is the first id of a table created at or after t, by binary search
// on the primary key: about 21 index lookups for 2 million rows. table is one
// of our constants, never input.
func firstID(ctx context.Context, tx pgx.Tx, table string, t time.Time) (int64, error) {
	var lo, hi int64
	if err := tx.QueryRow(ctx, `SELECT COALESCE(min(id), 0), COALESCE(max(id), 0) FROM `+table).Scan(&lo, &hi); err != nil {
		return 0, err
	}
	hi++ // hi: no row at or after it is old enough to matter
	for lo < hi {
		mid := lo + (hi-lo)/2
		var id int64
		var at time.Time
		err := tx.QueryRow(ctx, `SELECT id, created_at FROM `+table+` WHERE id >= $1 ORDER BY id LIMIT 1`, mid).Scan(&id, &at)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && !at.Before(t)) {
			hi = mid
			continue
		}
		if err != nil {
			return 0, err
		}
		lo = id + 1
	}
	return lo, nil
}

// readFor runs fn in a read-only transaction with a short time limit: these
// queries run on a visit.
func (s *store) readFor(ctx context.Context, fn func(pgx.Tx) error) error {
	return s.readLimit(ctx, "5s", fn)
}

// me computes the numbers of one account. A missing or suspended account is errNoAccount.
func (s *store) me(ctx context.Context, id int64, now time.Time, c config, b *bounds) (*Me, error) {
	m := &Me{At: now, Periods: map[string]MeCounts{}}
	err := s.readFor(ctx, func(tx pgx.Tx) error {
		var ava string
		err := tx.QueryRow(ctx, meAccountSQL, id).Scan(&m.ID, &m.User, &m.Name, &ava, &m.Since,
			&m.HidesGraph, &m.Posts, &m.Followers, &m.Following, &m.CanPublic)
		if errors.Is(err, pgx.ErrNoRows) {
			return errNoAccount
		}
		if err != nil {
			return fmt.Errorf("account: %w", err)
		}
		if m.Name = cleanName(m.Name); m.Name == "" {
			m.Name = m.User
		}
		m.URL = c.instance + "/@" + m.User
		m.Avatar = avatarURL(c.media, c.instance, m.ID, ava, false)

		since := map[string]time.Time{}
		for _, p := range periods {
			since[p] = periodStart(p, now, c.loc).UTC()
		}
		first, err := b.get(ctx, tx, since["m"], now)
		if err != nil {
			return fmt.Errorf("bounds: %w", err)
		}
		cnt := make(map[string]*MeCounts, len(periods))
		for _, p := range periods {
			cnt[p] = &MeCounts{}
		}
		// per period: count(*) FILTER (WHERE <col> >= $2/$3/$4)
		filters := func(col, extra string) string {
			var f []string
			for i := range periods {
				f = append(f, fmt.Sprintf("count(*) FILTER (WHERE %s >= $%d%s)", col, i+2, extra))
			}
			return strings.Join(f, ", ")
		}
		scan := func(row pgx.Row, set ...func(p string) *int64) error {
			dest := []any{}
			for _, f := range set {
				for _, p := range periods {
					dest = append(dest, f(p))
				}
			}
			return row.Scan(dest...)
		}
		sf := []any{id}
		ts := []any{id}
		for _, p := range periods {
			sf = append(sf, snowflake(since[p]))
			ts = append(ts, since[p])
		}

		// What anyone may see: public or unlisted, not local-only (one of our constants).
		anyone := " AND visibility <= 1"
		if s.localOnly {
			anyone += " AND local_only IS NOT TRUE"
		}
		// Own posts and boosts (status ids carry their time).
		err = scan(tx.QueryRow(ctx, `SELECT `+filters("id", " AND reblog_of_id IS NULL")+`, `+filters("id", " AND reblog_of_id IS NOT NULL")+`,
			`+filters("id", " AND reblog_of_id IS NULL"+anyone)+`
			FROM statuses WHERE account_id = $1 AND id >= $4 AND deleted_at IS NULL AND visibility < 3`, sf...),
			func(p string) *int64 { return &cnt[p].Posts }, func(p string) *int64 { return &cnt[p].BoostsGiven },
			func(p string) *int64 { return &cnt[p].PubPosts })
		if err != nil {
			return fmt.Errorf("posts: %w", err)
		}
		// Received, from notifications: what the person sees.
		rows, err := tx.Query(ctx, `SELECT type, `+filters("created_at", "")+` FROM notifications
			WHERE account_id = $1 AND id >= $5 AND created_at >= $4`+s.notifExtra+` AND type IN ('reblog', 'favourite', 'mention')
			GROUP BY type`, append(ts, first["notifications"])...)
		if err != nil {
			return fmt.Errorf("notifications: %w", err)
		}
		for rows.Next() {
			var typ string
			var n [3]int64
			if err := rows.Scan(&typ, &n[0], &n[1], &n[2]); err != nil {
				rows.Close()
				return err
			}
			for i, p := range periods {
				switch typ {
				case "reblog":
					cnt[p].BoostsGot = n[i]
				case "favourite":
					cnt[p].FavsGot = n[i]
				case "mention":
					cnt[p].Mentions = n[i]
				}
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		// Favs received on posts anyone may see: notification -> favourite -> post.
		err = scan(tx.QueryRow(ctx, `SELECT `+filters("n.created_at", "")+` FROM notifications n
			JOIN favourites f ON f.id = n.activity_id AND n.activity_type = 'Favourite'
			JOIN statuses st ON st.id = f.status_id
			WHERE n.account_id = $1 AND n.id >= $5 AND n.created_at >= $4`+s.notifExtra+` AND n.type = 'favourite'
			AND st.deleted_at IS NULL`+strings.ReplaceAll(anyone, " AND ", " AND st."), append(ts, first["notifications"])...),
			func(p string) *int64 { return &cnt[p].PubFavsGot })
		if err != nil {
			return fmt.Errorf("public favourites: %w", err)
		}
		err = scan(tx.QueryRow(ctx, `SELECT `+filters("created_at", "")+` FROM favourites
			WHERE account_id = $1 AND id >= $5 AND created_at >= $4`, append(ts, first["favourites"])...),
			func(p string) *int64 { return &cnt[p].FavsGiven })
		if err != nil {
			return fmt.Errorf("favourites: %w", err)
		}
		// follows: a few thousand rows per account at most, through its indexes.
		err = scan(tx.QueryRow(ctx, `SELECT `+filters("created_at", "")+` FROM follows
			WHERE target_account_id = $1 AND created_at >= $4`, ts...), func(p string) *int64 { return &cnt[p].Followers })
		if err != nil {
			return fmt.Errorf("followers: %w", err)
		}
		err = scan(tx.QueryRow(ctx, `SELECT `+filters("created_at", "")+` FROM follows
			WHERE account_id = $1 AND created_at >= $4`, ts...), func(p string) *int64 { return &cnt[p].Following })
		if err != nil {
			return fmt.Errorf("following: %w", err)
		}
		for _, p := range periods {
			m.Periods[p] = *cnt[p]
		}

		if m.Talkers, m.PublicMentions, err = s.talkers(ctx, tx, ts, first["notifications"], c); err != nil {
			return fmt.Errorf("talkers: %w", err)
		}
		if m.Links, err = s.myLinks(ctx, tx, id, snowflake(since["m"])); err != nil {
			return fmt.Errorf("links: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return m, nil
}

// talkers: who mentioned the account, local or not, suspended accounts left
// out; also the mentions that came in public or unlisted posts, per period. A
// mention notification points at its mentions row, which points at the post.
func (s *store) talkers(ctx context.Context, tx pgx.Tx, ts []any, first int64, c config) ([]MeTalker, map[string]int64, error) {
	pubPost := "st.visibility <= 1"
	if s.localOnly { // a local-only post isn't for the open internet either
		pubPost += " AND st.local_only IS NOT TRUE"
	}
	rows, err := tx.Query(ctx, `WITH n AS (SELECT n.from_account_id AS id, n.created_at AS at, COALESCE(`+pubPost+`, false) AS pub
			FROM notifications n
			LEFT JOIN mentions mn ON mn.id = n.activity_id AND n.activity_type = 'Mention'
			LEFT JOIN statuses st ON st.id = mn.status_id AND st.deleted_at IS NULL
			WHERE n.account_id = $1 AND n.id >= $5 AND n.created_at >= $4`+s.notifExtra+` AND n.type = 'mention'),
		m AS (SELECT id, count(*) FILTER (WHERE at >= $2) AS d, count(*) FILTER (WHERE at >= $3) AS w, count(*) AS mo,
			count(*) FILTER (WHERE pub AND at >= $2) AS pd, count(*) FILTER (WHERE pub AND at >= $3) AS pw, count(*) FILTER (WHERE pub) AS pm
			FROM n GROUP BY 1)
		SELECT a.id, a.username, COALESCE(a.domain, ''), COALESCE(a.display_name, ''), COALESCE(a.avatar_file_name, ''),
			COALESCE(a.url, a.uri, ''), m.d, m.w, m.mo, m.pd, m.pw, m.pm,
			CASE WHEN a.domain IS NULL THEN (`+localOK+`) ELSE (`+remoteOK+`) END,
			sum(m.pd) OVER (), sum(m.pw) OVER (), sum(m.pm) OVER ()
		FROM m JOIN accounts a ON a.id = m.id
		WHERE a.suspended_at IS NULL AND a.id <> $1
		ORDER BY m.mo DESC, a.id LIMIT 100`, append(ts, first)...)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	out := []MeTalker{}
	pub := map[string]int64{"d": 0, "w": 0, "m": 0}
	for rows.Next() {
		var (
			id                       int64
			user, dom, name, ava, at string
			d, w, mo, pd, pw, pm     int64
			sd, sw, sm               int64
			t                        MeTalker
		)
		if err := rows.Scan(&id, &user, &dom, &name, &ava, &at, &d, &w, &mo, &pd, &pw, &pm, &t.Shown, &sd, &sw, &sm); err != nil {
			return nil, nil, err
		}
		pub = map[string]int64{"d": sd, "w": sw, "m": sm}
		t.Acct, t.Name = user, cleanName(name)
		if t.Name == "" {
			t.Name = user
		}
		t.URL = at
		if dom != "" {
			t.Acct = user + "@" + dom
		}
		if dom == "" || t.URL == "" {
			t.URL = c.instance + "/@" + user
		}
		t.Avatar = avatarURL(c.media, c.instance, id, ava, dom != "")
		t.Counts = map[string]int64{"d": d, "w": w, "m": mo}
		t.Public = map[string]int64{"d": pd, "w": pw, "m": pm}
		out = append(out, t)
	}
	return out, pub, rows.Err()
}

// myLinks: the newest links of the account's own posts (no direct messages).
func (s *store) myLinks(ctx context.Context, tx pgx.Tx, id, since int64) ([]MeLink, error) {
	rows, err := tx.Query(ctx, `SELECT pc.url, COALESCE(pc.title, ''), s.created_at, (`+postOK+s.postExtra+`)
		FROM statuses s JOIN preview_cards_statuses pcs ON pcs.status_id = s.id JOIN preview_cards pc ON pc.id = pcs.preview_card_id
		WHERE s.account_id = $1 AND s.id >= $2 AND s.deleted_at IS NULL AND s.reblog_of_id IS NULL AND s.visibility < 3
		AND COALESCE(pc.title, '') <> ''
		ORDER BY s.id DESC LIMIT 40`, id, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []MeLink{}
	for rows.Next() {
		var l MeLink
		if err := rows.Scan(&l.URL, &l.Title, &l.At, &l.Public); err != nil {
			return nil, err
		}
		u, err := url.Parse(l.URL)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") {
			continue
		}
		l.Domain = strings.TrimPrefix(u.Hostname(), "www.")
		l.Title = clip(l.Title, 110)
		out = append(out, l)
	}
	return out, rows.Err()
}

// count: how many mentions a talker made in a period, all or only in public
// and unlisted posts.
func (t MeTalker) count(period string, public bool) int64 {
	if public {
		return t.Public[period]
	}
	return t.Counts[period]
}

// topTalkers: the people to show for a period, at most n. The public page only
// counts mentions made in public, so a direct message never puts anyone there.
func topTalkers(all []MeTalker, period string, public bool, n int) []MeTalker {
	out := []MeTalker{}
	for _, t := range all {
		if t.count(period, public) > 0 && (t.Shown || !public) {
			out = append(out, t)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].count(period, public) > out[j].count(period, public) })
	if len(out) > n {
		out = out[:n]
	}
	return out
}

// shownLinks: up to n links, each URL once; the public page only from public posts.
func shownLinks(all []MeLink, public bool, n int) []MeLink {
	out := []MeLink{}
	seen := map[string]bool{}
	for _, l := range all {
		if (public && !l.Public) || seen[l.URL] {
			continue
		}
		seen[l.URL] = true
		out = append(out, l)
		if len(out) == n {
			break
		}
	}
	return out
}

// meCache keeps each account's numbers for a few minutes and lets one
// computation run at a time: visits never put more than one query at a time
// on the database.
type meCache struct {
	compute func(ctx context.Context, id int64) (*Me, error)
	now     func() time.Time
	sem     chan struct{}

	mu   sync.Mutex
	m    map[int64]*Me
	gone map[int64]time.Time // accounts that weren't found, so a stale public URL doesn't query each visit
}

func newMeCache(compute func(ctx context.Context, id int64) (*Me, error)) *meCache {
	return &meCache{compute: compute, now: time.Now, sem: make(chan struct{}, 1), m: map[int64]*Me{}, gone: map[int64]time.Time{}}
}

var errBusy = errors.New("busy")

const (
	meFresh     = 5 * time.Minute  // /minhaconta
	meStale     = time.Hour        // /minhaconta when the database is slow: one's own numbers
	publicFresh = 15 * time.Minute // public pages, never older
)

// get returns numbers no older than maxAge, computing them when needed. When a
// computation fails or waits too long, numbers up to maxStale old are better
// than none; past that it is an error. Public pages pass maxStale = maxAge, so
// an opt-out (#nonumeros, a closed page) is never hidden behind a slow database.
func (c *meCache) get(ctx context.Context, id int64, maxAge, maxStale time.Duration) (*Me, error) {
	usable := func(m *Me) bool { return m != nil && c.now().Sub(m.At) < maxStale }
	if c.isGone(id, maxAge) {
		return nil, errNoAccount
	}
	if m := c.cached(id); m != nil && c.now().Sub(m.At) < maxAge {
		return m, nil
	}
	wait := time.NewTimer(10 * time.Second)
	defer wait.Stop()
	select {
	case c.sem <- struct{}{}:
	case <-wait.C:
		if m := c.cached(id); usable(m) {
			return m, nil
		}
		return nil, errBusy
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-c.sem }()
	if m := c.cached(id); m != nil && c.now().Sub(m.At) < maxAge { // computed while we waited
		return m, nil
	}
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	m, err := c.compute(cctx, id)
	if err != nil {
		if errors.Is(err, errNoAccount) {
			c.forget(id)
			c.mu.Lock()
			c.gone[id] = c.now()
			c.mu.Unlock()
			return nil, err
		}
		if old := c.cached(id); usable(old) {
			return old, nil
		}
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.m)+len(c.gone) > 5000 { // drop what's past any use
		for k, v := range c.m {
			if c.now().Sub(v.At) > meStale {
				delete(c.m, k)
			}
		}
		for k, at := range c.gone {
			if c.now().Sub(at) > meStale {
				delete(c.gone, k)
			}
		}
	}
	delete(c.gone, id)
	c.m[id] = m
	return m, nil
}

// isGone: the account wasn't found less than maxAge ago (it may come back, e.g.
// unsuspended, so the answer is kept no longer than numbers would be).
func (c *meCache) isGone(id int64, maxAge time.Duration) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	at, ok := c.gone[id]
	return ok && c.now().Sub(at) < maxAge
}

func (c *meCache) cached(id int64) *Me {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.m[id]
}

func (c *meCache) forget(id int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.m, id)
}
