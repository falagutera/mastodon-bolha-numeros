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
				"status": map[string]any{"phase": phase, "startTimestamp": st, "completionTimestamp": end, "progress": map[string]any{"itemsBackedUp": 5199}}}
		}
		json.NewEncoder(w).Encode(map[string]any{"items": []any{b("old", 48*time.Hour, "Completed"), b("new", time.Hour, "Completed"), b("mid", 24*time.Hour, "Completed"), b("broken", 10*time.Minute, "PartiallyFailed")}})
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
	if b.RetentionDays != 30 || b.DailyUTC != "04:00" || b.Count != 4 || !b.LastOK || b.LastTook != 13*time.Minute ||
		!b.LastAt.Equal(now.Add(-time.Hour)) || b.Items != 5199 || b.AppBytes != 1500 || b.TotalBytes != 101500 {
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
			json.NewEncoder(w).Encode(map[string]any{"items": []any{lb(time.Minute, "IN_PROGRESS", 3), lb(2*time.Minute, "CANCELED", 2), lb(30*time.Minute, "FAILED", 5), lb(time.Hour, "COMPLETED", 1024)}, "nextCursor": "p2"})
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
	// "Last" is the newest completed one: running, canceled and failed are skipped.
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
			LastTook: 13 * time.Minute, Count: 31, Items: 5199, AppBytes: 20 << 20, TotalBytes: 58 << 30},
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
		"31 backups", "Objetos do k8s", "5.199 (cluster inteiro)", "Dados dos volumes (inst.example)", "20,0 MB por backup", "58,0 GB por backup", "postgresql · databasus", "diária, 00:00",
		`class="st bad"`, "ontem às 00:00 · falhou", "7 backups, criptografados", "15,0 GB (banco de 95,0 GB)", "108 GB", "Outro", "sem dados nesta rodada", `class="two bkgrid"`, "numeros.css?v="} {
		if !strings.Contains(page, want) {
			t.Errorf("page lacks %q", want)
		}
	}
}

func TestPlatformVersions(t *testing.T) {
	for in, want := range map[string]string{"v1.36.5+rke2r1": "kubernetes-rke2 1.36.5", "v1.31.2+k3s1": "kubernetes-k3s 1.31.2",
		"v1.30.4-eks-a737599": "kubernetes-eks 1.30.4", "v1.30.5-gke.1014001": "kubernetes-gke 1.30.5", "v1.32.0": "kubernetes 1.32.0"} {
		if k, v := kubeDistro(in); k+" "+v != want {
			t.Errorf("kubeDistro(%q) = %s %s", in, k, v)
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /version", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"gitVersion":"v1.36.5+rke2r1"}`) })
	mux.HandleFunc("GET /apis/management.cattle.io/v3/settings/server-version", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"value":"v2.15.2"}`)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()
	tok := filepath.Join(t.TempDir(), "token")
	os.WriteFile(tok, []byte("t"), 0o600)
	items, err := platformVersions(context.Background(), &kubeAPI{base: ts.URL, tokenFile: tok, client: http.DefaultClient}, true)
	if err != nil || len(items) != 2 || items[0] != (Item{Key: "kubernetes-rke2", Value: "1.36.5"}) || items[1] != (Item{Key: "rancher", Value: "2.15.2"}) {
		t.Errorf("platform: %+v %v", items, err)
	}
}
