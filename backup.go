package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Backup is one card of the backup section: how long backups are kept, when
// they run, how the last one went and how big they are. Sizes that a tool
// doesn't report stay 0 and are left off the page.
type Backup struct {
	Source        string        `json:"source"` // module id: "velero", "databasus"
	Title         string        `json:"title"`
	TitleEN       string        `json:"title_en"`
	Sub           string        `json:"sub"`
	RetentionDays int           `json:"retention_days"` // 0: not a time period (see RetentionKeep)
	RetentionKeep int           `json:"retention_keep"` // keeps the newest N backups
	DailyUTC      string        `json:"daily_utc"`      // "04:00" when it runs once a day
	LastAt        time.Time     `json:"last_at"`
	LastOK        bool          `json:"last_ok"`
	LastTook      time.Duration `json:"last_took_ns"`
	Count         int64         `json:"count"`
	Encrypted     bool          `json:"encrypted"`

	AppBytes   int64 `json:"app_bytes,omitempty"`   // velero: the instance's volumes in the last backup
	TotalBytes int64 `json:"total_bytes,omitempty"` // velero: every volume in the last backup
	LastBytes  int64 `json:"last_bytes,omitempty"`  // databasus: the last dump
	DBBytes    int64 `json:"db_bytes,omitempty"`    // databasus: the database before the dump
	Stored     int64 `json:"stored_bytes,omitempty"`
}

// dailyAt reads "M H * * *" as a daily run at H:M (UTC, as Velero and cron use).
func dailyAt(cron string) string {
	f := strings.Fields(cron)
	if len(f) != 5 || f[2] != "*" || f[3] != "*" || f[4] != "*" {
		return ""
	}
	m, err1 := strconv.Atoi(f[0])
	h, err2 := strconv.Atoi(f[1])
	if err1 != nil || err2 != nil || m < 0 || m > 59 || h < 0 || h > 23 {
		return ""
	}
	return fmt.Sprintf("%02d:%02d", h, m)
}

// --- Velero, through the Kubernetes API with the pod's service account -------

type kubeAPI struct {
	base      string
	tokenFile string
	client    *http.Client
}

// inCluster builds the client from the service account mounted in the pod.
func inCluster() (*kubeAPI, error) {
	host, port := os.Getenv("KUBERNETES_SERVICE_HOST"), os.Getenv("KUBERNETES_SERVICE_PORT")
	if host == "" || port == "" {
		return nil, errors.New("not running in a cluster")
	}
	const sa = "/var/run/secrets/kubernetes.io/serviceaccount/"
	ca, err := os.ReadFile(sa + "ca.crt")
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca) {
		return nil, errors.New("service account: bad ca.crt")
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	return &kubeAPI{base: "https://" + host + ":" + port, tokenFile: sa + "token",
		client: &http.Client{Timeout: 30 * time.Second, Transport: tr}}, nil
}

func (k *kubeAPI) get(ctx context.Context, path string, out any) error {
	tok, err := os.ReadFile(k.tokenFile) // projected tokens rotate: read it every time
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, k.base+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(tok)))
	req.Header.Set("Accept", "application/json")
	resp, err := k.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		return fmt.Errorf("kubernetes %s: status %d", strings.SplitN(path, "?", 2)[0], resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(out)
}

type veleroBackup struct {
	Metadata struct {
		Name    string    `json:"name"`
		Created time.Time `json:"creationTimestamp"`
	} `json:"metadata"`
	Status struct {
		Phase string     `json:"phase"`
		Start *time.Time `json:"startTimestamp"`
		End   *time.Time `json:"completionTimestamp"`
	} `json:"status"`
}

// velero reads one schedule: its TTL and cron, the backups it made, and the
// volume sizes of the newest completed one (all of them, and appNS's).
func velero(ctx context.Context, k *kubeAPI, ns, schedule, appNS string) (*Backup, error) {
	base := "/apis/velero.io/v1/namespaces/" + url.PathEscape(ns)
	var sch struct {
		Spec struct {
			Schedule string `json:"schedule"`
			Template struct {
				TTL string `json:"ttl"`
			} `json:"template"`
		} `json:"spec"`
	}
	if err := k.get(ctx, base+"/schedules/"+url.PathEscape(schedule), &sch); err != nil {
		return nil, err
	}
	b := &Backup{DailyUTC: dailyAt(sch.Spec.Schedule)}
	if ttl, err := time.ParseDuration(sch.Spec.Template.TTL); err == nil {
		b.RetentionDays = int(ttl.Hours() / 24)
	}
	var list struct {
		Items []veleroBackup `json:"items"`
	}
	sel := url.Values{"labelSelector": {"velero.io/schedule-name=" + schedule}}
	if err := k.get(ctx, base+"/backups?"+sel.Encode(), &list); err != nil {
		return nil, err
	}
	if len(list.Items) == 0 {
		return b, nil
	}
	sort.Slice(list.Items, func(i, j int) bool { return list.Items[i].Metadata.Created.After(list.Items[j].Metadata.Created) })
	b.Count = int64(len(list.Items))
	last := list.Items[0]
	b.LastAt = last.Metadata.Created
	if last.Status.Start != nil {
		b.LastAt = *last.Status.Start
	}
	b.LastOK = last.Status.Phase == "Completed"
	if last.Status.Start != nil && last.Status.End != nil {
		b.LastTook = last.Status.End.Sub(*last.Status.Start)
	}
	// Sizes from the newest backup that finished.
	for _, it := range list.Items {
		if it.Status.Phase != "Completed" {
			continue
		}
		var pvbs struct {
			Items []struct {
				Spec struct {
					Pod struct {
						Namespace string `json:"namespace"`
					} `json:"pod"`
				} `json:"spec"`
				Status struct {
					Progress struct {
						TotalBytes int64 `json:"totalBytes"`
					} `json:"progress"`
				} `json:"status"`
			} `json:"items"`
		}
		sel := url.Values{"labelSelector": {"velero.io/backup-name=" + it.Metadata.Name}}
		if err := k.get(ctx, base+"/podvolumebackups?"+sel.Encode(), &pvbs); err != nil {
			return nil, err
		}
		for _, p := range pvbs.Items {
			b.TotalBytes += p.Status.Progress.TotalBytes
			if p.Spec.Pod.Namespace == appNS {
				b.AppBytes += p.Status.Progress.TotalBytes
			}
		}
		break
	}
	return b, nil
}

// --- Databasus, through its API with a viewer account ------------------------

type databasusAPI struct {
	base, email, password string
	client                *http.Client
	token                 string
}

func (d *databasusAPI) do(ctx context.Context, method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, d.base+"/api/v1"+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if d.token != "" {
		req.Header.Set("Authorization", "Bearer "+d.token)
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		return fmt.Errorf("databasus %s: status %d", strings.SplitN(path, "?", 2)[0], resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(out)
}

var periodDays = map[string]int{"DAY": 1, "WEEK": 7, "MONTH": 30, "THREE_MONTHS": 90, "SIX_MONTHS": 182,
	"YEAR": 365, "TWO_YEARS": 730, "THREE_YEARS": 1095, "FOUR_YEARS": 1461, "FIVE_YEARS": 1826}

// backups reads one database's backup config and its logical backups.
func (d *databasusAPI) backups(ctx context.Context, name string) (*Backup, error) {
	var sess struct {
		Token string `json:"token"`
	}
	d.token = ""
	if err := d.do(ctx, http.MethodPost, "/sessions", map[string]string{"email": d.email, "password": d.password}, &sess); err != nil {
		return nil, err
	}
	if sess.Token == "" {
		return nil, errors.New("databasus: no session")
	}
	d.token = sess.Token
	defer func() { d.token = "" }()

	var ws struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	if err := d.do(ctx, http.MethodGet, "/workspaces", nil, &ws); err != nil {
		return nil, err
	}
	id := ""
	for _, w := range ws.Items {
		cursor := ""
		for page := 0; page < 20 && id == ""; page++ {
			q := url.Values{"workspaceId": {w.ID}, "limit": {"100"}}
			if cursor != "" {
				q.Set("after", cursor)
			}
			var dbs struct {
				Items []struct {
					ID   string `json:"id"`
					Name string `json:"name"`
				} `json:"items"`
				Next string `json:"nextCursor"`
			}
			if err := d.do(ctx, http.MethodGet, "/databases?"+q.Encode(), nil, &dbs); err != nil {
				return nil, err
			}
			for _, x := range dbs.Items {
				if x.Name == name {
					id = x.ID
				}
			}
			if cursor = dbs.Next; cursor == "" {
				break
			}
		}
	}
	if id == "" {
		return nil, fmt.Errorf("databasus: no database %q visible", name)
	}

	var cfg struct {
		Encryption string `json:"encryption"`
		Settings   struct {
			Schedule struct {
				Type      string `json:"type"`
				TimeOfDay string `json:"timeOfDay"`
			} `json:"schedule"`
			Retention struct {
				Policy string `json:"policy"`
				Period string `json:"period"`
				Count  int    `json:"count"`
			} `json:"retention"`
		} `json:"settings"`
	}
	if err := d.do(ctx, http.MethodGet, "/backup-configs/"+url.PathEscape(id), nil, &cfg); err != nil {
		return nil, err
	}
	b := &Backup{Encrypted: cfg.Encryption == "ENCRYPTED"}
	if cfg.Settings.Schedule.Type == "DAILY" {
		b.DailyUTC = cfg.Settings.Schedule.TimeOfDay
	}
	switch cfg.Settings.Retention.Policy {
	case "TIME_PERIOD":
		b.RetentionDays = periodDays[cfg.Settings.Retention.Period]
	case "COUNT":
		b.RetentionKeep = cfg.Settings.Retention.Count
	}

	type logical struct {
		Created  time.Time `json:"createdAt"`
		Status   string    `json:"status"`
		Duration int64     `json:"durationMs"`
		Size     float64   `json:"sizeMb"`
		Raw      float64   `json:"rawDatabaseSizeMb"`
	}
	var all []logical
	cursor := ""
	for page := 0; page < 20; page++ {
		q := url.Values{"databaseId": {id}, "limit": {"100"}}
		if cursor != "" {
			q.Set("after", cursor)
		}
		var lb struct {
			Items []logical `json:"items"`
			Next  string    `json:"nextCursor"`
		}
		if err := d.do(ctx, http.MethodGet, "/logical-backups?"+q.Encode(), nil, &lb); err != nil {
			return nil, err
		}
		all = append(all, lb.Items...)
		if cursor = lb.Next; cursor == "" {
			break
		}
	}
	const mb = 1 << 20
	for _, x := range all { // newest first
		if x.Status != "COMPLETED" {
			continue
		}
		b.Count++
		b.Stored += int64(x.Size * mb)
	}
	if len(all) > 0 {
		last := all[0]
		if last.Status == "IN_PROGRESS" && len(all) > 1 {
			last = all[1] // the one that's running has no outcome yet
		}
		b.LastAt, b.LastOK, b.LastTook = last.Created, last.Status == "COMPLETED", time.Duration(last.Duration)*time.Millisecond
		b.LastBytes, b.DBBytes = int64(last.Size*mb), int64(last.Raw*mb)
	}
	return b, nil
}

// --- modules -------------------------------------------------------------------

// backupModule is one backup tool. A new tool is a type with collect and an
// entry in backupModules; the core only knows about the Backup it returns.
type backupModule interface {
	collect(ctx context.Context) (*Backup, error)
}

// backupSource is a configured module plus the texts of its card.
type backupSource struct {
	id                  string
	mod                 backupModule
	title, titleEN, sub string
}

type moduleDefaults struct{ title, titleEN, sub string }

// backupModules builds each module from its own NUMEROS_<ID>_* variables.
var backupModules = map[string]struct {
	defaults moduleDefaults
	build    func(c config, getenv func(string) string) (backupModule, error)
}{
	"velero": {
		defaults: moduleDefaults{"Aplicação", "Application", "kubernetes · velero"},
		build: func(c config, getenv func(string) string) (backupModule, error) {
			m := &veleroModule{ns: or(getenv("NUMEROS_VELERO_NAMESPACE"), "velero"), schedule: getenv("NUMEROS_VELERO_SCHEDULE"),
				appNS: getenv("NUMEROS_VELERO_APP_NAMESPACE")}
			if m.schedule == "" {
				return nil, errors.New("velero: NUMEROS_VELERO_SCHEDULE is not set")
			}
			k, err := inCluster()
			if err != nil {
				return nil, fmt.Errorf("velero: %w", err)
			}
			m.kube = k
			return m, nil
		},
	},
	"databasus": {
		defaults: moduleDefaults{"Banco de dados", "Database", "postgresql · databasus"},
		build: func(c config, getenv func(string) string) (backupModule, error) {
			m := &databasusModule{database: getenv("NUMEROS_DATABASUS_DATABASE"), api: &databasusAPI{
				base: strings.TrimRight(getenv("NUMEROS_DATABASUS_URL"), "/"), email: getenv("NUMEROS_DATABASUS_EMAIL"),
				password: getenv("NUMEROS_DATABASUS_PASSWORD"), client: &http.Client{Timeout: 30 * time.Second}}}
			if p, err := url.Parse(m.api.base); err != nil || (p.Scheme != "http" && p.Scheme != "https") || p.Host == "" {
				return nil, fmt.Errorf("databasus: NUMEROS_DATABASUS_URL %q is not a URL", m.api.base)
			}
			if m.api.email == "" || m.api.password == "" || m.database == "" {
				return nil, errors.New("databasus: needs NUMEROS_DATABASUS_EMAIL, _PASSWORD and _DATABASE")
			}
			return m, nil
		},
	},
}

type veleroModule struct {
	kube                *kubeAPI
	ns, schedule, appNS string
}

func (m *veleroModule) collect(ctx context.Context) (*Backup, error) {
	return velero(ctx, m.kube, m.ns, m.schedule, m.appNS)
}

type databasusModule struct {
	api      *databasusAPI
	database string
}

func (m *databasusModule) collect(ctx context.Context) (*Backup, error) {
	return m.api.backups(ctx, m.database)
}

// backupSources reads NUMEROS_BACKUP_SOURCES ("velero databasus": the cards,
// in order) and builds each module. Titles: NUMEROS_<ID>_TITLE, _TITLE_EN, _SUB.
func backupSources(c config, getenv func(string) string) ([]backupSource, error) {
	var out []backupSource
	for _, id := range strings.Fields(strings.ReplaceAll(getenv("NUMEROS_BACKUP_SOURCES"), ",", " ")) {
		def, ok := backupModules[id]
		if !ok {
			return nil, fmt.Errorf("config: unknown backup source %q", id)
		}
		mod, err := def.build(c, getenv)
		if err != nil {
			return nil, err
		}
		up := "NUMEROS_" + strings.ToUpper(id)
		out = append(out, backupSource{id: id, mod: mod, title: or(getenv(up+"_TITLE"), def.defaults.title),
			titleEN: or(getenv(up+"_TITLE_EN"), def.defaults.titleEN), sub: or(getenv(up+"_SUB"), def.defaults.sub)})
	}
	return out, nil
}

func or(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return strings.TrimSpace(v)
}
