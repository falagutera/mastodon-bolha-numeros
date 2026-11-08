package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// The extra numbers of /minhaconta (0.5.0): media, best posts, how and when the
// person posts, who they give to, polls, hashtags and bookmarks. Only the person
// sees these: the public /@user page never reads them. They are computed in a
// transaction of their own, after the main numbers, and kept 5 minutes.

// MePost: one of the person's posts, for the best-posts card.
type MePost struct {
	ID                    int64
	URL, Text             string
	Vis                   string // public, unlisted, private, local
	CW, Media             bool
	Favs, Boosts, Replies int64
	At                    time.Time
}

type MeMore struct {
	At time.Time

	// media of the last 30 days
	Media          map[string]int64 // image, video, gifv, audio
	AltDone, AltOf int64            // images and videos with a description, of all
	Photos         []Photo          // newest 9, not from direct or sensitive posts
	MediaBytes     int64            // everything the account uploaded and still has, originals

	// best posts of the last 30 days (no direct messages)
	Top     []MePost
	Replied *MePost

	// how the person posted in the last 30 days (no boosts)
	Vis                                map[string]int64 // public, unlisted, private, direct, local
	CW, Expiring, Edited, Replies, New int64            // replies and new: not direct
	DMsGot                             int64

	// when: weekday (Sunday 0) x hour, local time, 30 days; streaks over 90 days
	Heat                           [7][24]int64
	Langs, Apps                    []Bar
	Streak, BestStreak, ActiveDays int

	// the other side, 30 days
	FavTo, BoostTo []Person
	NewFollowers   []Person
	Quiet          []Person // followed for over 30 days, no fav, boost or mention from them in 30
	QuietN         int64

	// polls of the last 90 days
	Polls, PollVotes int64
	TopPoll          *Poll
	TopPollCW        bool // TopPoll.Text is the content warning

	// need the extra grants (tags, statuses_tags, bookmarks, poll_votes); zero when missing
	Tags                      []Bar
	Bookmarks, BookmarksMonth int64
	BookmarkServers           []Bar
	VotesGiven                int64
}

func (m *MeMore) stamp() time.Time { return m.At }

const moreDays = 90 // the window of the streaks and polls

// optional runs fn in a savepoint: a table the database user may not read
// (the grants of 0.5.0) leaves that part empty instead of failing the page.
func optional(ctx context.Context, tx pgx.Tx, what string, warned *sync.Map, fn func(pgx.Tx) error) error {
	sp, err := tx.Begin(ctx)
	if err != nil {
		return err
	}
	if err := fn(sp); err != nil {
		sp.Rollback(ctx)
		var pe *pgconn.PgError
		if errors.As(err, &pe) && (pe.Code == "42501" || pe.Code == "42P01") { // no grant, no table
			if _, seen := warned.LoadOrStore(what, true); !seen {
				log.Printf("minhaconta: %s left out: %s", what, pe.Message)
			}
			return nil
		}
		return fmt.Errorf("%s: %w", what, err)
	}
	return sp.Commit(ctx)
}

var moreWarned sync.Map

// more computes the extra numbers of one account.
func (s *store) more(ctx context.Context, id int64, now time.Time, c config, b *bounds) (*MeMore, error) {
	m := &MeMore{At: now, Media: map[string]int64{}, Vis: map[string]int64{}}
	month := now.Add(-30 * 24 * time.Hour).UTC()
	long := now.Add(-moreDays * 24 * time.Hour).UTC()
	err := s.readFor(ctx, func(tx pgx.Tx) error {
		var user string
		var ok bool
		err := tx.QueryRow(ctx, `SELECT a.username, true FROM accounts a WHERE a.id = $1 AND a.domain IS NULL AND a.suspended_at IS NULL`, id).Scan(&user, &ok)
		if errors.Is(err, pgx.ErrNoRows) {
			return errNoAccount
		}
		if err != nil {
			return fmt.Errorf("account: %w", err)
		}
		post := func(sid int64) string { return fmt.Sprintf("%s/@%s/%d", c.instance, user, sid) }
		first, err := b.get(ctx, tx, month, now)
		if err != nil {
			return fmt.Errorf("bounds: %w", err)
		}
		if err := s.ownPosts(ctx, tx, id, now, month, long, c, m); err != nil {
			return fmt.Errorf("posts: %w", err)
		}
		if err := s.ownMedia(ctx, tx, id, month, c, post, m); err != nil {
			return fmt.Errorf("media: %w", err)
		}
		// Direct messages that came in: the count only, never who.
		err = tx.QueryRow(ctx, `SELECT count(*) FROM notifications n
			JOIN mentions mn ON mn.id = n.activity_id AND n.activity_type = 'Mention'
			JOIN statuses st ON st.id = mn.status_id
			WHERE n.account_id = $1 AND n.id >= $2 AND n.created_at >= $3`+s.notifExtra+` AND n.type = 'mention'
			AND st.visibility = 3 AND st.deleted_at IS NULL`, id, first["notifications"], month).Scan(&m.DMsGot)
		if err != nil {
			return fmt.Errorf("direct: %w", err)
		}
		if err := s.otherSide(ctx, tx, id, month, first, c, m); err != nil {
			return err
		}
		if err := s.ownPolls(ctx, tx, id, long, post, m); err != nil {
			return fmt.Errorf("polls: %w", err)
		}
		if err := optional(ctx, tx, "best posts (status_stats)", &moreWarned, func(tx pgx.Tx) error {
			return s.bestPosts(ctx, tx, id, month, post, m)
		}); err != nil {
			return err
		}
		if err := optional(ctx, tx, "hashtags (tags, statuses_tags)", &moreWarned, func(tx pgx.Tx) error {
			var err error
			m.Tags, err = bars(ctx, tx, `SELECT COALESCE(NULLIF(t.display_name, ''), t.name), count(*) FROM statuses s
				JOIN statuses_tags st ON st.status_id = s.id JOIN tags t ON t.id = st.tag_id
				WHERE s.account_id = $1 AND s.id >= $2 AND s.deleted_at IS NULL AND s.reblog_of_id IS NULL AND s.visibility < 3
				GROUP BY 1 ORDER BY 2 DESC, 1 LIMIT 10`, id, snowflake(month))
			return err
		}); err != nil {
			return err
		}
		if err := optional(ctx, tx, "bookmarks", &moreWarned, func(tx pgx.Tx) error {
			err := tx.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE created_at >= $2) FROM bookmarks WHERE account_id = $1`,
				id, month).Scan(&m.Bookmarks, &m.BookmarksMonth)
			if err != nil {
				return err
			}
			m.BookmarkServers, err = bars(ctx, tx, `SELECT COALESCE(a.domain, $2), count(*) FROM bookmarks b
				JOIN statuses s ON s.id = b.status_id JOIN accounts a ON a.id = s.account_id
				WHERE b.account_id = $1 AND s.deleted_at IS NULL GROUP BY 1 ORDER BY 2 DESC, 1 LIMIT 5`, id, c.name)
			return err
		}); err != nil {
			return err
		}
		return optional(ctx, tx, "poll votes (poll_votes)", &moreWarned, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT count(DISTINCT poll_id) FROM poll_votes WHERE account_id = $1 AND created_at >= $2`,
				id, month).Scan(&m.VotesGiven)
		})
	})
	if err != nil {
		return nil, err
	}
	return m, nil
}

func bars(ctx context.Context, tx pgx.Tx, sql string, args ...any) ([]Bar, error) {
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

// ownPosts: one pass over the person's own posts of the last 90 days (a few
// thousand rows at most), counted here: visibility, style, hours, languages,
// apps and streaks.
func (s *store) ownPosts(ctx context.Context, tx pgx.Tx, id int64, now, month, long time.Time, c config, m *MeMore) error {
	local, expires := "false", "false"
	if s.localOnly {
		local = "s.local_only IS TRUE"
	}
	if s.expires {
		expires = "s.expires_at IS NOT NULL"
	}
	rows, err := tx.Query(ctx, `SELECT s.created_at, s.visibility, `+local+`, COALESCE(s.spoiler_text, '') <> '', `+expires+`,
		s.edited_at IS NOT NULL, s.in_reply_to_id IS NOT NULL, COALESCE(NULLIF(s.language, ''), '?'), COALESCE(o.name, '')
		FROM statuses s LEFT JOIN oauth_applications o ON o.id = s.application_id
		WHERE s.account_id = $1 AND s.id >= $2 AND s.deleted_at IS NULL AND s.reblog_of_id IS NULL`, id, snowflake(long))
	if err != nil {
		return err
	}
	defer rows.Close()
	langs, apps := map[string]int64{}, map[string]int64{}
	days := map[string]bool{}
	for rows.Next() {
		var (
			at                                time.Time
			vis                               int
			localOnly, cw, exp, edited, reply bool
			lang, app                         string
		)
		if err := rows.Scan(&at, &vis, &localOnly, &cw, &exp, &edited, &reply, &lang, &app); err != nil {
			return err
		}
		at = at.UTC()
		lt := at.In(c.loc)
		days[lt.Format("2006-01-02")] = true
		if at.Before(month) {
			continue
		}
		switch {
		case localOnly:
			m.Vis["local"]++
		case vis == 0:
			m.Vis["public"]++
		case vis == 1:
			m.Vis["unlisted"]++
		case vis == 2:
			m.Vis["private"]++
		default:
			m.Vis["direct"]++
		}
		m.Heat[lt.Weekday()][lt.Hour()]++
		if cw {
			m.CW++
		}
		if exp {
			m.Expiring++
		}
		if edited {
			m.Edited++
		}
		if vis == 3 {
			continue // what follows is about posts others read
		}
		if reply {
			m.Replies++
		} else {
			m.New++
		}
		langs[strings.ToLower(lang)]++
		if app != "" {
			apps[app]++
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	m.Langs, m.Apps = topBars(langs, 6), topBars(apps, 6)
	m.ActiveDays = len(days)
	m.Streak, m.BestStreak = streaks(days, now.In(c.loc), moreDays)
	return nil
}

// topBars: the n biggest, ties by name.
func topBars(in map[string]int64, n int) []Bar {
	out := make([]Bar, 0, len(in))
	for k, v := range in {
		out = append(out, Bar{k, v})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Value != out[j].Value {
			return out[i].Value > out[j].Value
		}
		return out[i].Name < out[j].Name
	})
	if len(out) > n {
		out = out[:n]
	}
	return out
}

// streaks: the days in a row with a post up to today (yesterday counts, so the
// streak doesn't break before today's post), and the longest in the window.
func streaks(days map[string]bool, today time.Time, window int) (current, best int) {
	run := 0
	for i := window; i >= 0; i-- {
		if days[today.AddDate(0, 0, -i).Format("2006-01-02")] {
			run++
			if run > best {
				best = run
			}
		} else {
			run = 0
		}
	}
	d := today
	if !days[d.Format("2006-01-02")] {
		d = d.AddDate(0, 0, -1)
	}
	for days[d.Format("2006-01-02")] {
		current++
		d = d.AddDate(0, 0, -1)
	}
	return current, best
}

var mediaKinds = map[int]string{0: "image", 1: "gifv", 2: "video", 4: "audio"}

// ownMedia: the media of the last 30 days, and the space of everything kept.
func (s *store) ownMedia(ctx context.Context, tx pgx.Tx, id int64, month time.Time, c config, post func(int64) string, m *MeMore) error {
	rows, err := tx.Query(ctx, `SELECT m.id, m.type, COALESCE(m.file_file_name, ''), COALESCE(m.description, ''), s.id,
		s.visibility, s.sensitive IS TRUE OR COALESCE(s.spoiler_text, '') <> ''
		FROM media_attachments m JOIN statuses s ON s.id = m.status_id
		WHERE m.account_id = $1 AND m.status_id >= $2 AND s.deleted_at IS NULL AND s.reblog_of_id IS NULL
		ORDER BY m.status_id DESC, m.id`, id, snowflake(month))
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			mid, sid  int64
			typ, vis  int
			file, alt string
			hidden    bool
		)
		if err := rows.Scan(&mid, &typ, &file, &alt, &sid, &vis, &hidden); err != nil {
			return err
		}
		kind, ok := mediaKinds[typ]
		if !ok {
			continue
		}
		m.Media[kind]++
		if kind != "audio" {
			m.AltOf++
			if strings.TrimSpace(alt) != "" {
				m.AltDone++
			}
		}
		// The grid: no direct messages and nothing behind a warning, it may be on a shared screen.
		if kind == "image" && file != "" && vis < 3 && !hidden && len(m.Photos) < 9 {
			m.Photos = append(m.Photos, Photo{Src: mediaURL(c.media, mid, file, "small"), Alt: clip(alt, 300), URL: post(sid)})
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return tx.QueryRow(ctx, `SELECT COALESCE(sum(COALESCE(file_file_size, 0)::bigint + COALESCE(thumbnail_file_size, 0)), 0)
		FROM media_attachments WHERE account_id = $1`, id).Scan(&m.MediaBytes)
}

// bestPosts: the top 5 by favs and boosts, and the one with most replies.
func (s *store) bestPosts(ctx context.Context, tx pgx.Tx, id int64, month time.Time, post func(int64) string, m *MeMore) error {
	local := "false"
	if s.localOnly {
		local = "s.local_only IS TRUE"
	}
	q := func(order string, limit int) ([]MePost, error) {
		rows, err := tx.Query(ctx, `SELECT s.id, s.visibility, `+local+`, COALESCE(s.spoiler_text, ''), COALESCE(s.text, ''), s.created_at,
			COALESCE(ss.favourites_count, 0), COALESCE(ss.reblogs_count, 0), COALESCE(ss.replies_count, 0),
			EXISTS (SELECT 1 FROM media_attachments ma WHERE ma.status_id = s.id)
			FROM statuses s JOIN status_stats ss ON ss.status_id = s.id
			WHERE s.account_id = $1 AND s.id >= $2 AND s.deleted_at IS NULL AND s.reblog_of_id IS NULL AND s.visibility < 3
			AND `+order+` > 0 ORDER BY `+order+` DESC, s.id DESC LIMIT `+fmt.Sprint(limit), id, snowflake(month))
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		out := []MePost{}
		for rows.Next() {
			var p MePost
			var vis int
			var localOnly bool
			var spoiler, text string
			if err := rows.Scan(&p.ID, &vis, &localOnly, &spoiler, &text, &p.At, &p.Favs, &p.Boosts, &p.Replies, &p.Media); err != nil {
				return nil, err
			}
			p.URL = post(p.ID)
			p.Vis = [...]string{"public", "unlisted", "private"}[vis]
			if localOnly {
				p.Vis = "local"
			}
			// Behind a warning: only the warning, as Mastodon shows it.
			if p.CW = spoiler != ""; p.CW {
				p.Text = clip(spoiler, 140)
			} else {
				p.Text = clip(stripTags(text), 140)
			}
			out = append(out, p)
		}
		return out, rows.Err()
	}
	var err error
	if m.Top, err = q("(COALESCE(ss.favourites_count, 0) + COALESCE(ss.reblogs_count, 0))", 5); err != nil {
		return err
	}
	r, err := q("COALESCE(ss.replies_count, 0)", 1)
	if err != nil {
		return err
	}
	if len(r) == 1 {
		m.Replied = &r[0]
	}
	return nil
}

// people reads accounts: id, username, domain, display name, avatar, url, value.
func people(ctx context.Context, tx pgx.Tx, c config, sql string, args ...any) ([]Person, error) {
	rows, err := tx.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Person{}
	for rows.Next() {
		var (
			aid                       int64
			user, dom, name, ava, url string
			p                         Person
		)
		if err := rows.Scan(&aid, &user, &dom, &name, &ava, &url, &p.Value); err != nil {
			return nil, err
		}
		p.Acct, p.Name, p.URL = user, cleanName(name), url
		if p.Name == "" {
			p.Name = user
		}
		if dom != "" {
			p.Acct = user + "@" + dom
		}
		if dom == "" || p.URL == "" {
			p.URL = c.instance + "/@" + user
		}
		p.Avatar = avatarURL(c.media, c.instance, aid, ava, dom != "")
		out = append(out, p)
	}
	return out, rows.Err()
}

const personCols = `a.id, a.username, COALESCE(a.domain, ''), COALESCE(a.display_name, ''), COALESCE(a.avatar_file_name, ''), COALESCE(a.url, a.uri, '')`

// otherSide: who the person favs and boosts the most, who started following
// them, and whom they follow without hearing back.
func (s *store) otherSide(ctx context.Context, tx pgx.Tx, id int64, month time.Time, first map[string]int64, c config, m *MeMore) error {
	var err error
	if m.FavTo, err = people(ctx, tx, c, `SELECT `+personCols+`, count(*) FROM favourites f
		JOIN statuses s ON s.id = f.status_id JOIN accounts a ON a.id = s.account_id
		WHERE f.account_id = $1 AND f.id >= $2 AND f.created_at >= $3 AND a.id <> $1 AND a.suspended_at IS NULL
		GROUP BY a.id ORDER BY 7 DESC, a.username, a.id LIMIT 10`, id, first["favourites"], month); err != nil {
		return fmt.Errorf("favs given to: %w", err)
	}
	if m.BoostTo, err = people(ctx, tx, c, `SELECT `+personCols+`, count(*) FROM statuses r
		JOIN statuses o ON o.id = r.reblog_of_id JOIN accounts a ON a.id = o.account_id
		WHERE r.account_id = $1 AND r.id >= $2 AND r.reblog_of_id IS NOT NULL AND r.deleted_at IS NULL
		AND a.id <> $1 AND a.suspended_at IS NULL
		GROUP BY a.id ORDER BY 7 DESC, a.username, a.id LIMIT 10`, id, snowflake(month)); err != nil {
		return fmt.Errorf("boosts given to: %w", err)
	}
	if m.NewFollowers, err = people(ctx, tx, c, `SELECT `+personCols+`, 0::bigint FROM follows f
		JOIN accounts a ON a.id = f.account_id
		WHERE f.target_account_id = $1 AND f.created_at >= $2 AND a.suspended_at IS NULL
		ORDER BY f.created_at DESC, a.id LIMIT 12`, id, month); err != nil {
		return fmt.Errorf("new followers: %w", err)
	}
	// Followed for over 30 days and nothing from them in 30: the oldest first.
	// The value column carries how many there are in all.
	if m.Quiet, err = people(ctx, tx, c, `WITH heard AS (SELECT DISTINCT from_account_id AS id FROM notifications
			WHERE account_id = $1 AND id >= $2 AND created_at >= $3`+s.notifExtra+`
			AND type IN ('favourite', 'reblog', 'mention') AND from_account_id IS NOT NULL)
		SELECT `+personCols+`, count(*) OVER () FROM follows f JOIN accounts a ON a.id = f.target_account_id
		WHERE f.account_id = $1 AND f.created_at < $3 AND a.suspended_at IS NULL
		AND NOT EXISTS (SELECT 1 FROM heard h WHERE h.id = a.id)
		ORDER BY f.created_at, a.id LIMIT 12`, id, first["notifications"], month); err != nil {
		return fmt.Errorf("quiet: %w", err)
	}
	if len(m.Quiet) > 0 {
		m.QuietN = m.Quiet[0].Value
	}
	for i := range m.Quiet {
		m.Quiet[i].Value = 0
	}
	return nil
}

// ownPolls: the polls of the last 90 days, their votes and the most voted.
func (s *store) ownPolls(ctx context.Context, tx pgx.Tx, id int64, long time.Time, post func(int64) string, m *MeMore) error {
	err := tx.QueryRow(ctx, `SELECT count(*), COALESCE(sum(p.votes_count), 0) FROM polls p JOIN statuses s ON s.id = p.status_id
		WHERE p.account_id = $1 AND p.created_at >= $2 AND s.deleted_at IS NULL`, id, long).Scan(&m.Polls, &m.PollVotes)
	if err != nil {
		return err
	}
	// The most voted: never one sent as a direct message, and behind a warning
	// only the warning shows, as in the best posts.
	var p Poll
	var sid int64
	var spoiler string
	err = tx.QueryRow(ctx, `SELECT s.id, COALESCE(s.spoiler_text, ''), COALESCE(s.text, ''), COALESCE(p.votes_count, 0)
		FROM polls p JOIN statuses s ON s.id = p.status_id
		WHERE p.account_id = $1 AND p.created_at >= $2 AND s.deleted_at IS NULL AND s.visibility < 3 AND COALESCE(p.votes_count, 0) > 0
		ORDER BY p.votes_count DESC, p.id DESC LIMIT 1`, id, long).Scan(&sid, &spoiler, &p.Text, &p.Votes)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	p.Text, p.URL = clip(stripTags(p.Text), 140), post(sid)
	if spoiler != "" {
		p.Text, m.TopPollCW = clip(spoiler, 140), true
	}
	m.TopPoll = &p
	return nil
}

// moreCompute is what the extra cache runs.
func moreCompute(st *store, cfg config) func(ctx context.Context, id int64) (*MeMore, error) {
	b := &bounds{}
	return func(ctx context.Context, id int64) (*MeMore, error) {
		return st.more(ctx, id, time.Now(), cfg, b)
	}
}
