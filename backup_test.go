package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDailyAt(t *testing.T) {
	for in, want := range map[string]string{"0 4 * * *": "04:00", "30 23 * * *": "23:30", "0 4 * * 1": "", "*/5 * * * *": "", "x": ""} {
		if got := dailyAt(in); got != want {
			t.Errorf("dailyAt(%q) = %q", in, got)
		}
	}
}

func TestVelero(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /apis/velero.io/v1/namespaces/velero/schedules/daily-all", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sa-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		fmt.Fprint(w, `{"spec":{"schedule":"0 4 * * *","template":{"ttl":"720h0m0s"}}}`)
	})
	busy := 1 // the first list gets a 429, as the API server's fairness may answer
	mux.HandleFunc("GET /apis/velero.io/v1/namespaces/velero/backups", func(w http.ResponseWriter, r *http.Request) {
		if busy > 0 {
			busy--
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		if r.URL.Query().Get("labelSelector") != "velero.io/schedule-name=daily-all" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		b := func(name string, ago time.Duration, phase string) map[string]any {
			st, end := now.Add(-ago), now.Add(-ago+13*time.Minute)
			return map[string]any{"metadata": map[string]any{"name": name, "creationTimestamp": st},
				"status": map[string]any{"phase": phase, "startTimestamp": st, "completionTimestamp": end}}
		}
		json.NewEncoder(w).Encode(map[string]any{"items": []any{b("old", 48*time.Hour, "Completed"), b("new", time.Hour, "Completed"), b("mid", 24*time.Hour, "Completed")}})
	})
	mux.HandleFunc("GET /apis/velero.io/v1/namespaces/velero/podvolumebackups", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("labelSelector") != "velero.io/backup-name=new" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		pvb := func(ns string, n int64) map[string]any {
			return map[string]any{"spec": map[string]any{"pod": map[string]any{"namespace": ns}}, "status": map[string]any{"progress": map[string]any{"totalBytes": n}}}
		}
		json.NewEncoder(w).Encode(map[string]any{"items": []any{pvb("bolha-us", 1000), pvb("bolha-us", 500), pvb("other", 100000)}})
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()
	tok := filepath.Join(t.TempDir(), "token")
	os.WriteFile(tok, []byte("sa-token\n"), 0o600)
	k := &kubeAPI{base: ts.URL, tokenFile: tok, client: http.DefaultClient}
	b, err := velero(context.Background(), k, "velero", "daily-all", "bolha-us")
	if err != nil {
		t.Fatal(err)
	}
	if b.RetentionDays != 30 || b.DailyUTC != "04:00" || b.Count != 3 || !b.LastOK || b.LastTook != 13*time.Minute ||
		!b.LastAt.Equal(now.Add(-time.Hour)) || b.AppBytes != 1500 || b.TotalBytes != 101500 {
		t.Errorf("velero: %+v", b)
	}
}

func TestDatabasus(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	mux := http.NewServeMux()
	auth := func(w http.ResponseWriter, r *http.Request) bool {
		if r.Header.Get("Authorization") != "Bearer s1" {
			w.WriteHeader(http.StatusUnauthorized)
			return false
		}
		return true
	}
	mux.HandleFunc("POST /api/v1/sessions", func(w http.ResponseWriter, r *http.Request) {
		var in map[string]string
		json.NewDecoder(r.Body).Decode(&in)
		if in["email"] != "numeros@x" || in["password"] != "pw" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		fmt.Fprint(w, `{"token":"s1"}`)
	})
	mux.HandleFunc("GET /api/v1/workspaces", func(w http.ResponseWriter, r *http.Request) {
		if auth(w, r) {
			fmt.Fprint(w, `{"items":[{"id":"w1"}]}`)
		}
	})
	mux.HandleFunc("GET /api/v1/databases", func(w http.ResponseWriter, r *http.Request) {
		if !auth(w, r) || r.URL.Query().Get("workspaceId") != "w1" {
			return
		}
		if r.URL.Query().Get("after") == "" { // two pages, the database on the second
			fmt.Fprint(w, `{"items":[{"id":"d0","name":"tango-pg-other"}],"nextCursor":"p2"}`)
			return
		}
		fmt.Fprint(w, `{"items":[{"id":"d1","name":"tango-pg-mastodon_production"}]}`)
	})
	mux.HandleFunc("GET /api/v1/backup-configs/d1", func(w http.ResponseWriter, r *http.Request) {
		if auth(w, r) {
			fmt.Fprint(w, `{"encryption":"ENCRYPTED","settings":{"schedule":{"type":"DAILY","timeOfDay":"03:00"},"retention":{"policy":"TIME_PERIOD","period":"WEEK","count":0}}}`)
		}
	})
	mux.HandleFunc("GET /api/v1/logical-backups", func(w http.ResponseWriter, r *http.Request) {
		if !auth(w, r) || r.URL.Query().Get("databaseId") != "d1" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		lb := func(ago time.Duration, status string, size float64) map[string]any {
			return map[string]any{"createdAt": now.Add(-ago), "status": status, "durationMs": 2249904, "sizeMb": size, "rawDatabaseSizeMb": 97467.86}
		}
		if r.URL.Query().Get("after") == "" {
			json.NewEncoder(w).Encode(map[string]any{"items": []any{lb(time.Minute, "IN_PROGRESS", 3), lb(2*time.Minute, "CANCELED", 2), lb(time.Hour, "COMPLETED", 1024)}, "nextCursor": "p2"})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"items": []any{lb(25*time.Hour, "FAILED", 10), lb(49*time.Hour, "COMPLETED", 2048)}})
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()
	d := &databasusAPI{base: ts.URL, email: "numeros@x", password: "pw", client: http.DefaultClient}
	b, err := d.backups(context.Background(), "tango-pg-mastodon_production")
	if err != nil {
		t.Fatal(err)
	}
	// Running and canceled backups have no outcome: "last" is the completed one before them.
	if b.RetentionDays != 7 || b.DailyUTC != "03:00" || !b.Encrypted || b.Count != 2 || !b.LastOK ||
		!b.LastAt.Equal(now.Add(-time.Hour)) || b.LastBytes != 1024<<20 || b.Stored != 3072<<20 || b.LastTook/time.Minute != 37 {
		t.Errorf("databasus: %+v", b)
	}
	if d.token != "" {
		t.Error("session token kept after the round")
	}
	if _, err := d.backups(context.Background(), "missing"); err == nil {
		t.Error("a database the viewer can't see should be an error")
	}
}

func TestBackupCards(t *testing.T) {
	cfg := testConfig()
	srv, err := newServer(cfg, &collector{cfg: cfg, now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 7, 15, 0, 0, 0, time.UTC)
	snap := testSnapshot(now)
	snap.Backups = []Backup{
		{Source: "velero", Title: "Aplicação", Sub: "kubernetes · velero", RetentionDays: 30, DailyUTC: "04:00", LastAt: time.Date(2026, 11, 7, 4, 0, 8, 0, time.UTC), LastOK: true,
			LastTook: 13 * time.Minute, Count: 31, AppBytes: 20 << 20, TotalBytes: 58 << 30},
		{Source: "databasus", Title: "Banco de dados", Sub: "postgresql · databasus", RetentionDays: 7, DailyUTC: "03:00", LastAt: time.Date(2026, 11, 6, 3, 0, 11, 0, time.UTC), LastOK: false,
			Count: 7, Encrypted: true, LastBytes: 15 << 30, DBBytes: 95 << 30, Stored: 108 << 30},
	}
	snap.Backups = append(snap.Backups, Backup{Source: "x", Title: "Outro", Sub: "y", Missing: true})
	v := srv.view("pt", snap, now)
	var b strings.Builder
	if err := srv.tmpl.ExecuteTemplate(&b, "page.html", v); err != nil {
		t.Fatal(err)
	}
	page := b.String()
	for _, want := range []string{"Backup", "kubernetes · velero", ">30<", "dias de retenção", "diária, 01:00", "hoje às 01:00 · 13 min",
		"31 backups", "20,0 MB por backup", "58,0 GB por backup", "postgresql · databasus", "diária, 00:00",
		`class="st bad"`, "ontem às 00:00 · falhou", "7 backups, criptografados", "15,0 GB (banco de 95,0 GB)", "108 GB", "Outro", "sem dados nesta rodada", `class="two bkgrid"`, "numeros.css?v="} {
		if !strings.Contains(page, want) {
			t.Errorf("page lacks %q", want)
		}
	}
}
