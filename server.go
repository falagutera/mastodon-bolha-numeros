package main

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"gitlab.gutocarvalho.net/open/bolha/numeros/internal/relnotes"
)

//go:embed templates/*.html templates/numeros.css
var templateFS embed.FS

type server struct {
	cfg  config
	col  *collector
	tmpl *template.Template
	css  []byte
	csp  string
}

func newServer(cfg config, col *collector) (*server, error) {
	tmpl, err := template.New("").Funcs(template.FuncMap{
		"raw": func(s string) template.HTML { return template.HTML(s) }, // only for our own strings
		"dict3": func(title, sub string, rows []barRow) map[string]any {
			return map[string]any{"Title": title, "Sub": sub, "Rows": rows}
		},
	}).ParseFS(templateFS, "templates/*.html")
	if err != nil {
		return nil, err
	}
	css, err := templateFS.ReadFile("templates/numeros.css")
	if err != nil {
		return nil, err
	}
	img := []string{"'self'"}
	for _, raw := range []string{cfg.media, cfg.instance} {
		if u, err := url.Parse(raw); err == nil && u.Host != "" {
			img = append(img, u.Scheme+"://"+u.Host)
		}
	}
	csp := "default-src 'none'; style-src 'self'; img-src " + strings.Join(img, " ") +
		"; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"
	return &server{cfg: cfg, col: col, tmpl: tmpl, css: css, csp: csp}, nil
}

func (s *server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) { s.page(w, "pt") })
	mux.HandleFunc("GET /en/{$}", func(w http.ResponseWriter, r *http.Request) { s.page(w, "en") })
	mux.HandleFunc("GET /en", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/en/", http.StatusMovedPermanently) })
	mux.HandleFunc("GET /numeros.css", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=3600")
		w.Write(s.css)
	})
	mux.HandleFunc("GET /numeros.json", s.json)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	relnotes.Register(mux, relnotes.PageOptions{App: "numeros", Domain: "bolha.us", Version: version,
		Source: "https://gitlab.gutocarvalho.net/open/bolha/numeros", LangKey: "numeros-lang", Style: string(s.css)}, releases)
	return headers(mux, s.csp)
}

func headers(next http.Handler, csp string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		if strings.HasPrefix(r.URL.Path, "/release-notes") {
			// The shared release-notes page carries its own inline style and language switch.
			h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
		} else {
			h.Set("Content-Security-Policy", csp)
		}
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}

func (s *server) json(w http.ResponseWriter, r *http.Request) {
	snap := s.col.snapshot()
	if snap == nil {
		http.Error(w, "collecting", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Cache-Control", "public, max-age=300")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.Encode(snap)
}

// --- view model -------------------------------------------------------------

type tile struct{ Label, Value, Sub string }

type rankRow struct {
	N           int
	Name, Acct  string
	URL, Avatar string
	Value, Sub  string
	Bar         int // 0-100
}

type rankCard struct {
	Title, Unit string
	Periods     []rankPeriod
}

type rankPeriod struct {
	Key  string
	Rows []rankRow
}

type barRow struct {
	Name, Value string
	Bar         int
}

type kv struct{ Key, Value string }

type view struct {
	Lang, Other, OtherHref string
	T                      map[string]string
	Ready                  bool
	Updated, Stale         string
	Tiles                  []tile
	Periods                []kv // key, label
	Ranks                  []rankCard
	Links                  []Link
	LinkSubs               []string
	Photos                 []Photo
	PhotoAlts              []string
	Polls                  []Poll
	PollSubs               []string
	NewUsers, Reports      []tile
	ReportsAll             string
	Clients, Languages     []barRow
	Talkers                []barRow
	Software, Space        []kv
	Version                string
}

func (s *server) page(w http.ResponseWriter, lang string) {
	v := s.view(lang, s.col.snapshot(), time.Now())
	var b bytes.Buffer
	if err := s.tmpl.ExecuteTemplate(&b, "page.html", v); err != nil {
		log.Printf("render: %v", err)
		http.Error(w, "render error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=60")
	w.Write(b.Bytes())
}

func (s *server) view(lang string, snap *Snapshot, now time.Time) view {
	t := strs[lang]
	v := view{Lang: lang, T: t, Other: t["lang_other"], OtherHref: t["lang_href"], Version: version}
	if snap == nil {
		return v
	}
	v.Ready = true
	n := func(x int64) string { return formatInt(x, lang) }
	loc := s.cfg.loc
	day := s.date(snap.At, lang)
	if snap.At.In(loc).Format("2006-01-02") == now.In(loc).Format("2006-01-02") {
		day = t["today"]
	}
	v.Updated = fmt.Sprintf(t["updated"], day, snap.At.In(loc).Format("15:04"), snap.Next.In(loc).Format("15:04"))
	if len(snap.Stale) > 0 {
		v.Stale = fmt.Sprintf(t["stale"], strings.Join(snap.Stale, ", "))
	}

	activeSub := ""
	if snap.Users > 0 && snap.Active >= 0 {
		activeSub = fmt.Sprintf(t["active_sub"], strconv.FormatInt(snap.Active*100/snap.Users, 10))
	}
	v.Tiles = []tile{
		{t["users"], n(snap.Users), t["users_sub"]},
		{t["active"], n(snap.Active), activeSub},
		{t["posts"], s.short(snap.Posts, lang), t["posts_sub"]},
		{t["servers"], n(snap.Servers), t["servers_sub"]},
		{t["since"], s.date(snap.Since, lang), age(snap.Since, now, lang)},
	}

	for _, p := range periods {
		v.Periods = append(v.Periods, kv{p, t["p_"+p]})
	}
	for _, r := range snap.Rankings {
		card := rankCard{Title: t["r_"+r.Key], Unit: t["u_"+r.Key]}
		for _, p := range periods {
			rp := rankPeriod{Key: p}
			people := r.Periods[p]
			for i, person := range people {
				row := rankRow{N: i + 1, Name: person.Name, Acct: "@" + person.Acct, URL: person.URL, Avatar: person.Avatar,
					Value: n(person.Value), Bar: pct(person.Value, people[0].Value)}
				if r.Totals {
					row.Sub = fmt.Sprintf(t["total"], n(person.Total))
				}
				rp.Rows = append(rp.Rows, row)
			}
			card.Periods = append(card.Periods, rp)
		}
		v.Ranks = append(v.Ranks, card)
	}

	v.Links = snap.Links
	for _, l := range snap.Links {
		v.LinkSubs = append(v.LinkSubs, fmt.Sprintf("%s @%s · %s", t["by"], l.Acct, s.ago(l.At, now, lang)))
	}
	v.Photos = snap.Photos
	for _, p := range snap.Photos {
		alt := p.Alt
		if alt == "" {
			alt = fmt.Sprintf(t["photo"], p.Acct)
		}
		v.PhotoAlts = append(v.PhotoAlts, alt)
	}
	v.Polls = snap.Polls
	for _, p := range snap.Polls {
		word := t["votes"]
		if p.Votes == 1 {
			word = t["vote"]
		}
		v.PollSubs = append(v.PollSubs, fmt.Sprintf("@%s · %s %s", p.Acct, n(p.Votes), word))
	}

	win := func(w Windows) []tile {
		return []tile{{t["w_week"], n(w.Week), ""}, {t["w_month"], n(w.Month), ""}, {t["w_half"], n(w.HalfYear), ""}, {t["w_year"], n(w.Year), ""}}
	}
	v.NewUsers, v.Reports = win(snap.NewUsers), win(snap.Reports)
	v.ReportsAll = fmt.Sprintf(t["reports_all"], n(snap.ReportsTotal))

	bars := func(in []Bar, name func(string) string) []barRow {
		out := []barRow{}
		for _, b := range in {
			out = append(out, barRow{Name: name(b.Name), Value: n(b.Value), Bar: pct(b.Value, in[0].Value)})
		}
		return out
	}
	same := func(x string) string { return x }
	v.Clients = bars(snap.Clients, same)
	v.Talkers = bars(snap.Talkers, same)
	v.Languages = bars(snap.Languages, func(code string) string {
		if name, ok := languageNames[lang][code]; ok {
			return name
		}
		return strings.ToUpper(code)
	})

	for _, it := range snap.Software {
		name := softwareNames[it.Key]
		if name == "" {
			name = it.Key
		}
		v.Software = append(v.Software, kv{name, it.Value})
	}
	for _, it := range snap.Space {
		name := t["sp_"+it.Key]
		if name == "" {
			name = it.Key
		}
		v.Space = append(v.Space, kv{name, formatBytes(it.Bytes, lang)})
	}
	return v
}

func pct(x, top int64) int {
	if top <= 0 {
		return 0
	}
	p := int(x * 100 / top)
	if p < 2 && x > 0 {
		p = 2
	}
	return p
}

// date: "22 mai 2022" / "May 22, 2022".
func (s *server) date(t time.Time, lang string) string {
	if t.IsZero() {
		return "—"
	}
	t = t.In(s.cfg.loc)
	m := monthNames[lang][t.Month()-1]
	if lang == "en" {
		return fmt.Sprintf("%s %d, %d", m, t.Day(), t.Year())
	}
	return fmt.Sprintf("%d %s %d", t.Day(), m, t.Year())
}

// short: 2312345 -> "2,31 mi" / "2.31M"; below a million, the full number.
func (s *server) short(x int64, lang string) string {
	if x < 1_000_000 {
		return formatInt(x, lang)
	}
	f := strconv.FormatFloat(float64(x)/1e6, 'f', 2, 64)
	if lang == "en" {
		return f + "M"
	}
	return strings.Replace(f, ".", ",", 1) + " mi"
}

func (s *server) ago(t, now time.Time, lang string) string {
	d := now.Sub(t)
	en := lang == "en"
	switch {
	case d < time.Hour:
		m := int(d.Minutes())
		if m < 1 {
			m = 1
		}
		if en {
			return fmt.Sprintf("%d min ago", m)
		}
		return fmt.Sprintf("há %d min", m)
	case d < 48*time.Hour:
		if en {
			return fmt.Sprintf("%d h ago", int(d.Hours()))
		}
		return fmt.Sprintf("há %d h", int(d.Hours()))
	}
	if en {
		return fmt.Sprintf("%d days ago", int(d.Hours()/24))
	}
	return fmt.Sprintf("há %d dias", int(d.Hours()/24))
}
