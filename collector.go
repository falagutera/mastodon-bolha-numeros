package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

// collector builds a snapshot every round and keeps the last one. A block that
// fails keeps the previous round's values and is listed as stale.
type collector struct {
	cfg     config
	db      *store
	admin   *adminAPI // nil: no admin token, those blocks are left out
	client  *http.Client
	backups []backupSource // the backup cards, in order
	kube    *kubeAPI       // NUMEROS_PLATFORM: Kubernetes/Rancher versions
	now     func() time.Time

	mu   sync.RWMutex
	last *Snapshot
}

func (c *collector) snapshot() *Snapshot {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.last
}

// load reads the snapshot saved by the previous run, so a restart shows numbers
// right away instead of waiting for the first round.
func (c *collector) load() {
	if c.cfg.snapshotPath == "" {
		return
	}
	raw, err := os.ReadFile(c.cfg.snapshotPath)
	if err != nil {
		return
	}
	var s Snapshot
	if err := json.Unmarshal(raw, &s); err == nil && !s.At.IsZero() {
		c.mu.Lock()
		c.last = &s
		c.mu.Unlock()
	}
}

func (c *collector) save(s *Snapshot) {
	if c.cfg.snapshotPath == "" {
		return
	}
	raw, err := json.Marshal(s)
	if err != nil {
		return
	}
	tmp := c.cfg.snapshotPath + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		log.Printf("snapshot: %v", err)
		return
	}
	if err := os.Rename(tmp, filepath.Clean(c.cfg.snapshotPath)); err != nil {
		log.Printf("snapshot: %v", err)
	}
}

// nextRound is the next time on the hour grid, offset from :00 so it doesn't
// land with the instance's own hourly jobs.
func nextRound(now time.Time, every, offset time.Duration) time.Time {
	t := now.Truncate(every).Add(offset)
	for !t.After(now) {
		t = t.Add(every)
	}
	return t
}

// run collects now if the saved snapshot is older than a round, then on the grid.
func (c *collector) run(ctx context.Context) {
	if s := c.snapshot(); s == nil || c.now().Sub(s.At) >= c.cfg.every {
		c.round(ctx)
	}
	for {
		next := nextRound(c.now(), c.cfg.every, c.cfg.offset)
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Until(next)):
			c.round(ctx)
		}
	}
}

func (c *collector) round(ctx context.Context) {
	start := c.now()
	prev := c.snapshot()
	s := &Snapshot{}
	if prev != nil {
		cp := *prev
		s = &cp
	}
	s.Stale = nil
	stale := func(block string, err error) {
		log.Printf("collect %s: %v", block, err)
		s.Stale = append(s.Stale, block)
	}
	block := func(name string, fn func(context.Context, pgx.Tx) error) {
		ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
		defer cancel()
		if err := c.db.read(ctx, func(tx pgx.Tx) error { return fn(ctx, tx) }); err != nil {
			stale(name, err)
		}
	}

	block("rankings", func(ctx context.Context, tx pgx.Tx) error {
		var out []Ranking
		for _, key := range rankingOrder {
			r := Ranking{Key: key, Periods: map[string][]Person{}, Totals: rankingSQL[key].total != ""}
			for _, p := range periods {
				people, err := c.db.ranking(ctx, tx, key, periodStart(p, start, c.cfg.loc), c.cfg)
				if err != nil {
					return err
				}
				r.Periods[p] = people
			}
			out = append(out, r)
		}
		s.Rankings = out
		return nil
	})
	block("content", func(ctx context.Context, tx pgx.Tx) error {
		links, photos, polls, err := c.db.content(ctx, tx, start, c.cfg)
		if err != nil {
			return err
		}
		s.Links, s.Photos, s.Polls = links, photos, polls
		return nil
	})
	block("server", func(ctx context.Context, tx pgx.Tx) error { return c.db.server(ctx, tx, s) })
	block("usage", func(ctx context.Context, tx pgx.Tx) error { return c.db.usage(ctx, tx, start, s, c.cfg) })
	var pgVersion string
	var pgSize int64
	block("database", func(ctx context.Context, tx pgx.Tx) (err error) {
		pgVersion, pgSize, err = c.db.database(ctx, tx)
		return err
	})

	if c.admin != nil {
		actx, cancel := context.WithTimeout(ctx, time.Minute)
		if n, err := c.admin.activeUsers(actx, start); err == nil {
			s.Active = n
		} else {
			stale("active", err) // the database's count of recent sign-ins stays
		}
		if sw, sp, err := c.admin.dimensions(actx, start); err == nil {
			s.Software, s.Space = sw, sp
		} else {
			stale("software", err)
		}
		cancel()
	}
	// Without the admin API: what the database alone can tell.
	if len(s.Software) == 0 && pgVersion != "" {
		s.Software = []Item{{Key: "postgresql", Value: pgVersion}}
	}
	if len(s.Space) == 0 && pgSize > 0 {
		s.Space = []Item{{Key: "postgresql", Bytes: pgSize}}
	}
	if c.cfg.searchURL != "" {
		sctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		if n, err := searchSize(sctx, c.client, c.cfg.searchURL, c.cfg.searchUser, c.cfg.searchPass, c.cfg.searchPrefix); err == nil {
			s.Space = setItem(s.Space, Item{Key: "search", Bytes: n})
		} else {
			stale("search", err)
		}
		cancel()
	}

	if c.kube != nil {
		pctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		rancher := slices.Contains(c.cfg.platform, "rancher")
		items, err := platformVersions(pctx, c.kube, rancher)
		cancel()
		if err != nil {
			stale("platform", err)
		}
		if len(items) == 0 && prev != nil { // keep the last known ones
			for _, it := range prev.Software {
				if strings.HasPrefix(it.Key, "kubernetes") || it.Key == "rancher" {
					items = append(items, it)
				}
			}
		}
		for _, it := range items {
			s.Software = setItem(s.Software, it)
		}
	}
	if len(c.backups) > 0 {
		var cards []Backup
		bctx, cancel := context.WithTimeout(ctx, time.Minute)
		for _, src := range c.backups {
			b, err := src.mod.collect(bctx)
			if err != nil {
				stale(src.id, err)
				if p := prevBackup(prev, src.id); len(p) > 0 {
					cards = append(cards, p...)
				} else { // keep the card in its place, so the section doesn't change shape
					cards = append(cards, Backup{Source: src.id, Title: src.title, TitleEN: src.titleEN, Sub: src.sub, Missing: true})
				}
				continue
			}
			b.Source, b.Title, b.TitleEN, b.Sub = src.id, src.title, src.titleEN, src.sub
			cards = append(cards, *b)
		}
		cancel()
		s.Backups = cards
	}

	s.At = start
	s.Took = c.now().Sub(start)
	s.Next = nextRound(start, c.cfg.every, c.cfg.offset)
	c.mu.Lock()
	c.last = s
	c.mu.Unlock()
	c.save(s)
	log.Printf("collected in %s, stale: %v", s.Took.Round(time.Millisecond), s.Stale)
}

func setItem(items []Item, it Item) []Item {
	for i := range items {
		if items[i].Key == it.Key {
			items[i] = it
			return items
		}
	}
	return append(items, it)
}

// prevBackup is the last round's card of a tool, kept when this round failed.
func prevBackup(prev *Snapshot, tool string) []Backup {
	if prev == nil {
		return nil
	}
	for _, b := range prev.Backups {
		if b.Source == tool {
			return []Backup{b}
		}
	}
	return nil
}
