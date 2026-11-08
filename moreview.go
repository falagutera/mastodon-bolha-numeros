package main

import (
	"fmt"
	"strings"
	"time"
)

// The extra cards of /minhaconta, from MeMore. Only fill() of the own page
// calls this; the public page has no More.

type postRow struct {
	URL, Text, Sub string
	CW             bool
}

type heatCell struct {
	Level int // 0-4
	Title string
}

type heatRow struct {
	Label string
	Cells []heatCell
}

type moreView struct {
	MediaTiles        []tile
	Photos            []Photo
	PhotoAlts         []string
	Top               []postRow
	Replied           *postRow
	VisTiles          []tile
	StyleTiles        []tile
	Langs, Apps, Tags []barRow
	Heat              []heatRow
	HeatHours         []string
	HeatNote          string
	RhythmTiles       []tile
	FavTo, BoostTo    []rankRow
	NewFollowers      []rankRow
	Quiet             []rankRow
	QuietN            string
	PollTiles         []tile
	TopPoll           *postRow
	Servers           []barRow
}

func (s *server) fillMore(m *MeMore, t map[string]string, lang string, now time.Time) *moreView {
	n := func(x int64) string { return formatInt(x, lang) }
	v := &moreView{}

	// media
	for _, k := range []string{"image", "video", "gifv", "audio"} {
		v.MediaTiles = append(v.MediaTiles, tile{t["mm_"+k], n(m.Media[k]), ""})
	}
	alt := "—"
	if m.AltOf > 0 {
		alt = fmt.Sprintf("%d%%", m.AltDone*100/m.AltOf)
	}
	v.MediaTiles = append(v.MediaTiles, tile{t["mm_alt"], alt, fmt.Sprintf(t["mm_alt_sub"], n(m.AltDone), n(m.AltOf))},
		tile{t["mm_space"], formatBytes(m.MediaBytes, lang), t["mm_space_sub"]})
	v.Photos = m.Photos
	for _, p := range m.Photos {
		a := p.Alt
		if a == "" {
			a = t["mm_noalt"]
		}
		v.PhotoAlts = append(v.PhotoAlts, a)
	}

	// best posts
	row := func(p MePost) postRow {
		sub := fmt.Sprintf(t["mp_sub"], n(p.Favs), n(p.Boosts), n(p.Replies)) + " · " + t["vis_"+p.Vis] + " · " + s.ago(p.At, now, lang)
		text := p.Text
		if text == "" {
			text = t["mp_notext"]
		}
		return postRow{URL: p.URL, Text: text, Sub: sub, CW: p.CW}
	}
	for _, p := range m.Top {
		v.Top = append(v.Top, row(p))
	}
	if m.Replied != nil {
		r := row(*m.Replied)
		v.Replied = &r
	}

	// how
	for _, k := range []string{"public", "unlisted", "private", "local", "direct"} {
		v.VisTiles = append(v.VisTiles, tile{t["vis_"+k], n(m.Vis[k]), ""})
	}
	v.StyleTiles = []tile{
		{t["ms_new"], n(m.New), ""}, {t["ms_replies"], n(m.Replies), ""},
		{t["ms_cw"], n(m.CW), ""}, {t["ms_edited"], n(m.Edited), ""},
		{t["ms_expiring"], n(m.Expiring), ""}, {t["ms_dms_got"], n(m.DMsGot), t["ms_dms_sub"]},
	}
	bars := func(in []Bar, name func(string) string) []barRow {
		out := []barRow{}
		for _, b := range in {
			out = append(out, barRow{Name: name(b.Name), Value: n(b.Value), Bar: pct(b.Value, in[0].Value)})
		}
		return out
	}
	same := func(x string) string { return x }
	v.Langs = bars(m.Langs, func(code string) string {
		if name, ok := languageNames[lang][code]; ok {
			return name
		}
		return strings.ToUpper(code)
	})
	v.Apps = bars(m.Apps, same)
	v.Tags = bars(m.Tags, func(x string) string { return "#" + x })
	v.Servers = bars(m.BookmarkServers, same)

	// when: Monday first
	var top int64
	for _, d := range m.Heat {
		for _, x := range d {
			top = max(top, x)
		}
	}
	for _, wd := range []int{1, 2, 3, 4, 5, 6, 0} {
		hr := heatRow{Label: t[fmt.Sprintf("wd_%d", wd)]}
		for h, x := range m.Heat[wd] {
			lv := 0
			if x > 0 && top > 0 {
				lv = 1 + int(x*3/top) // 1-4
				lv = min(lv, 4)
			}
			hr.Cells = append(hr.Cells, heatCell{lv, fmt.Sprintf(t["mh_cell"], hr.Label, h, n(x))})
		}
		v.Heat = append(v.Heat, hr)
	}
	v.HeatNote = fmt.Sprintf(t["mh_note"], s.cfg.loc.String())
	for h := 0; h < 24; h++ {
		lbl := ""
		if h%6 == 0 {
			lbl = fmt.Sprintf("%02d", h)
		}
		v.HeatHours = append(v.HeatHours, lbl)
	}
	v.RhythmTiles = []tile{
		{t["mr_streak"], n(int64(m.Streak)), t["mr_days"]},
		{t["mr_best"], n(int64(m.BestStreak)), fmt.Sprintf(t["mr_in"], moreDays)},
		{t["mr_active"], n(int64(m.ActiveDays)), fmt.Sprintf(t["mr_of"], moreDays)},
	}

	// the other side
	ranks := func(in []Person, values bool) []rankRow {
		out := []rankRow{}
		for i, p := range in {
			r := rankRow{N: i + 1, Name: p.Name, Acct: "@" + p.Acct, URL: p.URL, Avatar: p.Avatar}
			if values {
				r.Value, r.Bar = n(p.Value), pct(p.Value, in[0].Value)
			}
			out = append(out, r)
		}
		return out
	}
	v.FavTo, v.BoostTo = ranks(m.FavTo, true), ranks(m.BoostTo, true)
	v.NewFollowers, v.Quiet = ranks(m.NewFollowers, false), ranks(m.Quiet, false)
	v.QuietN = n(m.QuietN)

	// polls and bookmarks
	v.PollTiles = []tile{
		{t["mo_polls"], n(m.Polls), fmt.Sprintf(t["mr_in"], moreDays)},
		{t["mo_poll_votes"], n(m.PollVotes), t["mo_poll_votes_sub"]},
		{t["mo_votes_given"], n(m.VotesGiven), t["d30"]},
		{t["mo_bookmarks"], n(m.BookmarksMonth), fmt.Sprintf(t["mo_bookmarks_sub"], n(m.Bookmarks))},
	}
	if m.TopPoll != nil {
		word := t["votes"]
		if m.TopPoll.Votes == 1 {
			word = t["vote"]
		}
		text := m.TopPoll.Text
		if text == "" {
			text = t["mp_notext"]
		}
		v.TopPoll = &postRow{URL: m.TopPoll.URL, Text: text, Sub: n(m.TopPoll.Votes) + " " + word, CW: m.TopPollCW}
	}
	return v
}
