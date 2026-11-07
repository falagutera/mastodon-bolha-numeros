package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Snapshot is everything the page shows, gathered once per round. The page only
// ever reads the last snapshot, so a visit never touches the database.
type Snapshot struct {
	At   time.Time     `json:"at"`
	Next time.Time     `json:"next"`
	Took time.Duration `json:"took_ns"`

	Users   int64     `json:"users"`
	Active  int64     `json:"active_users"` // 30 days; -1 when unknown
	Posts   int64     `json:"posts"`
	Servers int64     `json:"known_servers"`
	Since   time.Time `json:"since"`

	Rankings []Ranking `json:"rankings"`

	Links  []Link  `json:"links"`
	Photos []Photo `json:"photos"`
	Polls  []Poll  `json:"polls"`

	NewUsers     Windows `json:"new_users"`
	Reports      Windows `json:"reports"`
	ReportsTotal int64   `json:"reports_total"`

	Clients   []Bar `json:"clients"`
	Languages []Bar `json:"languages"`
	Talkers   []Bar `json:"servers_talking"`

	Software []Item `json:"software"`
	Space    []Item `json:"space"`

	Backups []Backup `json:"backups,omitempty"` // application (velero), then database (databasus)

	// Blocks that failed in this round and kept the previous round's values.
	Stale []string `json:"stale,omitempty"`
}

// Windows counts over the last week, month, six months and year.
type Windows struct {
	Week, Month, HalfYear, Year int64
}

// Ranking is one "top 12" card, per period: today, 7 days, 30 days.
type Ranking struct {
	Key     string              `json:"key"`
	Periods map[string][]Person `json:"periods"` // "d", "w", "m"
	Totals  bool                `json:"totals"`  // people carry an all-time total
}

type Person struct {
	Acct   string `json:"acct"` // user, or user@domain for remote accounts
	Name   string `json:"name"`
	URL    string `json:"url"`
	Avatar string `json:"avatar"`
	Value  int64  `json:"value"`
	Total  int64  `json:"total,omitempty"`
}

type Link struct {
	URL    string    `json:"url"`
	Title  string    `json:"title"`
	Domain string    `json:"domain"`
	Acct   string    `json:"acct"`
	At     time.Time `json:"at"`
}

type Photo struct {
	Src  string `json:"src"`
	Alt  string `json:"alt"`
	URL  string `json:"url"` // the post
	Acct string `json:"acct"`
}

type Poll struct {
	Text  string `json:"text"`
	Acct  string `json:"acct"`
	Votes int64  `json:"votes"`
	URL   string `json:"url"`
}

type Bar struct {
	Name  string `json:"name"`
	Value int64  `json:"value"`
}

// Item is a name and a value already meant for display (a version), or a
// size in bytes.
type Item struct {
	Key   string `json:"key"`
	Value string `json:"value,omitempty"`
	Bytes int64  `json:"bytes,omitempty"`
}

var periods = []string{"d", "w", "m"}

// periodStart is when a period begins: today is since midnight in loc.
func periodStart(p string, now time.Time, loc *time.Location) time.Time {
	switch p {
	case "d":
		l := now.In(loc)
		return time.Date(l.Year(), l.Month(), l.Day(), 0, 0, 0, 0, loc)
	case "w":
		return now.Add(-7 * 24 * time.Hour)
	default:
		return now.Add(-30 * 24 * time.Hour)
	}
}

// snowflake is the smallest Mastodon status id created at t: the id carries
// the time in its upper bits, so "since t" is a range on the primary key.
func snowflake(t time.Time) int64 { return t.UnixMilli() << 16 }

// idPartition is Paperclip's path for an id: 108336787171335042 ->
// 108/336/787/171/335/042 (at least 9 digits).
func idPartition(id int64) string {
	s := fmt.Sprintf("%09d", id)
	if r := len(s) % 3; r != 0 {
		s = strings.Repeat("0", 3-r) + s
	}
	parts := make([]string, 0, len(s)/3)
	for i := 0; i < len(s); i += 3 {
		parts = append(parts, s[i:i+3])
	}
	return strings.Join(parts, "/")
}

// avatarURL rebuilds the avatar address from the file name, as Mastodon does:
// remote avatars live under cache/. No file: the instance's default.
func avatarURL(media, instance string, id int64, file string, remote bool) string {
	if file == "" {
		return instance + "/avatars/original/missing.png"
	}
	prefix := ""
	if remote {
		prefix = "cache/"
	}
	return fmt.Sprintf("%s/%saccounts/avatars/%s/original/%s", media, prefix, idPartition(id), file)
}

func mediaURL(media string, id int64, file, style string) string {
	return fmt.Sprintf("%s/media_attachments/files/%s/%s/%s", media, idPartition(id), style, file)
}

// formatInt groups thousands: 8412 -> "8.412" (pt) or "8,412" (en).
func formatInt(n int64, lang string) string {
	neg := n < 0
	if neg {
		n = -n
	}
	s := strconv.FormatInt(n, 10)
	sep := "."
	if lang == "en" {
		sep = ","
	}
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteString(sep)
		}
		b.WriteRune(c)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

// formatBytes: 91482624000 -> "85,2 GB" (pt) or "85.2 GB" (en), base 1024.
func formatBytes(n int64, lang string) string {
	units := []string{"B", "KB", "MB", "GB", "TB", "PB"}
	f := float64(n)
	i := 0
	for f >= 1024 && i < len(units)-1 {
		f /= 1024
		i++
	}
	var s string
	switch {
	case i == 0:
		s = strconv.FormatInt(n, 10)
	case f >= 100:
		s = strconv.FormatFloat(f, 'f', 0, 64)
	default:
		s = strconv.FormatFloat(f, 'f', 1, 64)
	}
	if lang != "en" {
		s = strings.Replace(s, ".", ",", 1)
	}
	return s + " " + units[i]
}

// age is how long since t, in years and months: "4 anos e 5 meses".
func age(t, now time.Time, lang string) string {
	if t.IsZero() {
		return ""
	}
	months := (now.Year()-t.Year())*12 + int(now.Month()-t.Month())
	if now.Day() < t.Day() {
		months--
	}
	if months < 0 {
		months = 0
	}
	y, m := months/12, months%12
	word := func(n int, one, many string) string {
		if n == 1 {
			return "1 " + one
		}
		return strconv.Itoa(n) + " " + many
	}
	if lang == "en" {
		ys, ms := word(y, "year", "years"), word(m, "month", "months")
		switch {
		case y == 0:
			return ms + " ago"
		case m == 0:
			return ys + " ago"
		}
		return ys + " and " + ms + " ago"
	}
	ys, ms := word(y, "ano", "anos"), word(m, "mês", "meses")
	switch {
	case y == 0:
		return "há " + ms
	case m == 0:
		return "há " + ys
	}
	return "há " + ys + " e " + ms
}

// clip shortens s to n runes, on a word when it can.
func clip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	cut := string(r[:n])
	if i := strings.LastIndex(cut, " "); i > n/2 {
		cut = cut[:i]
	}
	return strings.TrimRight(cut, " ,.;:") + "…"
}
