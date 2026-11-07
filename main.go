// numeros.bolha.us: a one-page view of an instance's numbers, read from the
// Mastodon database (read-only) and its admin dashboard API, once an hour.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata" // the image has no zone files
)

type config struct {
	listen       string
	instance     string // public URL of the instance
	internal     *url.URL
	media        string // where media and avatars are served from
	dbHost       string
	dbPort       string
	dbName       string
	dbUser       string
	dbPassword   string
	dbSSLMode    string
	adminToken   string
	searchURL    string
	searchUser   string
	searchPass   string
	searchPrefix string
	loc          *time.Location
	every        time.Duration
	offset       time.Duration
	snapshotPath string

	// What the page calls things; see README "Your instance".
	name                  string // "bolha.us"; default: the instance's host
	usPT, usEN            string // "a bolha" / "the bubble"; default "{name}"
	headlinePT            string // raw HTML for the <h1>; default "{name} em <em>números</em>."
	headlineEN            string
	mediaStorage          string // shown as "Mídia (X)"
	backupNotePT          string // note under the backup cards
	backupNoteEN          string
	parentName, parentURL string // "part of ..." in the footer
}

func env(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}

func loadConfig() (config, error) {
	c := config{
		listen:       env("NUMEROS_LISTEN", ":8080"),
		instance:     strings.TrimRight(env("NUMEROS_INSTANCE", "https://bolha.us"), "/"),
		dbHost:       env("NUMEROS_DB_HOST", "127.0.0.1"),
		dbPort:       env("NUMEROS_DB_PORT", "5432"),
		dbName:       env("NUMEROS_DB_NAME", "mastodon_production"),
		dbUser:       env("NUMEROS_DB_USER", "numeros"),
		dbPassword:   os.Getenv("NUMEROS_DB_PASSWORD"),
		dbSSLMode:    env("NUMEROS_DB_SSLMODE", "disable"),
		adminToken:   os.Getenv("NUMEROS_ADMIN_TOKEN"),
		searchURL:    env("NUMEROS_SEARCH_URL", ""),
		searchUser:   env("NUMEROS_SEARCH_USER", ""),
		searchPass:   os.Getenv("NUMEROS_SEARCH_PASSWORD"),
		searchPrefix: env("NUMEROS_SEARCH_PREFIX", ""),
		snapshotPath: env("NUMEROS_SNAPSHOT", ""),
	}
	pub, _ := url.Parse(c.instance)
	c.name = env("NUMEROS_NAME", pub.Host)
	c.usPT, c.usEN = env("NUMEROS_US", c.name), env("NUMEROS_US_EN", c.name)
	c.headlinePT, c.headlineEN = env("NUMEROS_HEADLINE", ""), env("NUMEROS_HEADLINE_EN", "")
	c.mediaStorage = env("NUMEROS_MEDIA_STORAGE", "")
	c.backupNotePT, c.backupNoteEN = env("NUMEROS_BACKUP_NOTE", ""), env("NUMEROS_BACKUP_NOTE_EN", "")
	c.parentName, c.parentURL = env("NUMEROS_PARENT_NAME", ""), env("NUMEROS_PARENT_URL", "")
	if c.parentURL != "" {
		if p, err := url.Parse(c.parentURL); err != nil || p.Scheme != "https" || p.Host == "" {
			return c, fmt.Errorf("config: NUMEROS_PARENT_URL %q must be an https URL", c.parentURL)
		}
	}
	c.media = strings.TrimRight(env("NUMEROS_MEDIA_URL", c.instance+"/system"), "/")
	for _, u := range []string{c.instance, c.media} {
		if p, err := url.Parse(u); err != nil || p.Scheme != "https" || p.Host == "" {
			return c, fmt.Errorf("config: %q must be an https URL", u)
		}
	}
	if raw := env("NUMEROS_INTERNAL_URL", ""); raw != "" {
		p, err := url.Parse(raw)
		if err != nil || (p.Scheme != "http" && p.Scheme != "https") || p.Host == "" {
			return c, fmt.Errorf("config: NUMEROS_INTERNAL_URL %q is not a URL", raw)
		}
		c.internal = p
	}
	if c.dbPassword == "" {
		return c, errors.New("config: NUMEROS_DB_PASSWORD is not set")
	}
	var err error
	if c.loc, err = time.LoadLocation(env("NUMEROS_TIMEZONE", "America/Sao_Paulo")); err != nil {
		return c, fmt.Errorf("config: timezone: %w", err)
	}
	if c.every, err = time.ParseDuration(env("NUMEROS_EVERY", "1h")); err != nil || c.every < 10*time.Minute {
		return c, errors.New("config: NUMEROS_EVERY must be a duration of at least 10m")
	}
	if c.offset, err = time.ParseDuration(env("NUMEROS_OFFSET", "7m")); err != nil || c.offset < 0 || c.offset >= c.every {
		return c, errors.New("config: NUMEROS_OFFSET must be between 0 and NUMEROS_EVERY")
	}
	return c, nil
}

func main() {
	healthcheck := flag.Bool("healthcheck", false, "probe the local server and exit")
	flag.Parse()
	if *healthcheck {
		port := env("NUMEROS_LISTEN", ":8080")
		if i := strings.LastIndex(port, ":"); i >= 0 {
			port = port[i:]
		}
		client := &http.Client{Timeout: 3 * time.Second}
		resp, err := client.Get("http://127.0.0.1" + port + "/healthz")
		if err != nil || resp.StatusCode != http.StatusOK {
			os.Exit(1)
		}
		return
	}
	cfg, err := loadConfig()
	if err != nil {
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	db, err := openStore(ctx, cfg)
	if err != nil {
		log.Fatal(err)
	}
	if err := db.schema(ctx); err != nil {
		log.Fatal(err)
	}
	pub, _ := url.Parse(cfg.instance)
	client := &http.Client{Timeout: 30 * time.Second,
		Transport: &internalTransport{public: pub, internal: cfg.internal, next: http.DefaultTransport}}
	col := &collector{cfg: cfg, db: db, client: client, now: time.Now}
	if cfg.adminToken != "" {
		col.admin = &adminAPI{base: cfg.instance, token: cfg.adminToken, client: client}
	}
	if col.backups, err = backupSources(cfg, func(k string) string { return strings.TrimSpace(os.Getenv(k)) }); err != nil {
		log.Fatal(err)
	}
	col.load()
	srv, err := newServer(cfg, col)
	if err != nil {
		log.Fatal(err)
	}
	go col.run(ctx)

	hs := &http.Server{Addr: cfg.listen, Handler: srv.routes(), ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout: 10 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		hs.Shutdown(sctx)
	}()
	log.Printf("numeros %s listening on %s for %s", version, cfg.listen, cfg.instance)
	if err := hs.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}
