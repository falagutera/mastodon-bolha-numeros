package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// adminAPI reads the instance's own dashboard numbers: software versions,
// space used and active users. The token needs admin:read on an account whose
// role can only view the dashboard.
type adminAPI struct {
	base   string // public instance URL, e.g. https://bolha.us
	token  string
	client *http.Client
}

type dimension struct {
	Key  string `json:"key"`
	Data []struct {
		Key        string          `json:"key"`
		HumanKey   string          `json:"human_key"`
		Value      json.RawMessage `json:"value"`
		HumanValue string          `json:"human_value"`
	} `json:"data"`
}

func (a *adminAPI) post(ctx context.Context, path string, form url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.base+path, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+a.token)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		return fmt.Errorf("%s: status %d", path, resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out)
}

// softwareOrder is how the versions are listed; anything else reported comes after.
var softwareOrder = []string{"mastodon", "ruby", "postgresql", "redis", "elasticsearch", "libvips", "imagemagick", "ffmpeg"}

// dimensions returns the software versions and the space used (postgresql,
// redis, media), as the admin dashboard shows them.
func (a *adminAPI) dimensions(ctx context.Context, now time.Time) (software, space []Item, err error) {
	form := url.Values{
		"keys[]":   {"software_versions", "space_usage"},
		"start_at": {now.Add(-24 * time.Hour).UTC().Format("2006-01-02")},
		"end_at":   {now.UTC().Format("2006-01-02")},
	}
	var dims []dimension
	if err := a.post(ctx, "/api/v1/admin/dimensions", form, &dims); err != nil {
		return nil, nil, err
	}
	for _, d := range dims {
		for _, x := range d.Data {
			switch d.Key {
			case "software_versions":
				v := strings.Trim(string(x.Value), `"`)
				if f := strings.Fields(v); x.Key == "ruby" && len(f) > 1 {
					v = f[1] // "ruby 4.0.9 (2026-10-23 revision …) +YJIT …" -> "4.0.9"
				}
				if v != "" && v != "null" {
					software = append(software, Item{Key: x.Key, Value: v})
				}
			case "space_usage":
				n, err := strconv.ParseInt(strings.Trim(string(x.Value), `"`), 10, 64)
				if err == nil {
					space = append(space, Item{Key: x.Key, Bytes: n})
				}
			}
		}
	}
	software = ordered(software, softwareOrder)
	space = ordered(space, []string{"postgresql", "redis", "media"})
	if len(software) == 0 {
		return nil, nil, fmt.Errorf("dimensions: no software versions")
	}
	return software, space, nil
}

// activeUsers is the dashboard's "active users" of the last 30 days.
func (a *adminAPI) activeUsers(ctx context.Context, now time.Time) (int64, error) {
	form := url.Values{
		"keys[]":   {"active_users"},
		"start_at": {now.Add(-30 * 24 * time.Hour).UTC().Format("2006-01-02")},
		"end_at":   {now.UTC().Format("2006-01-02")},
	}
	var ms []struct {
		Key   string `json:"key"`
		Total string `json:"total"`
	}
	if err := a.post(ctx, "/api/v1/admin/measures", form, &ms); err != nil {
		return 0, err
	}
	for _, m := range ms {
		if m.Key == "active_users" {
			return strconv.ParseInt(m.Total, 10, 64)
		}
	}
	return 0, fmt.Errorf("measures: no active_users")
}

func ordered(items []Item, order []string) []Item {
	out := make([]Item, 0, len(items))
	for _, k := range order {
		for _, it := range items {
			if it.Key == k {
				out = append(out, it)
			}
		}
	}
	for _, it := range items {
		known := false
		for _, k := range order {
			known = known || it.Key == k
		}
		if !known {
			out = append(out, it)
		}
	}
	return out
}

// searchSize is the disk used by the search indexes (OpenSearch or
// Elasticsearch), summed over the indexes with the prefix.
func searchSize(ctx context.Context, client *http.Client, base, user, pass, prefix string) (int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+"/_cat/indices?format=json&bytes=b&h=index,store.size", nil)
	if err != nil {
		return 0, err
	}
	if user != "" {
		req.SetBasicAuth(user, pass)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("search: status %d", resp.StatusCode)
	}
	var idx []struct {
		Index string `json:"index"`
		Size  string `json:"store.size"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&idx); err != nil {
		return 0, err
	}
	var total int64
	for _, i := range idx {
		if strings.HasPrefix(i.Index, ".") || !strings.HasPrefix(i.Index, prefix) {
			continue
		}
		n, _ := strconv.ParseInt(i.Size, 10, 64)
		total += n
	}
	return total, nil
}

// internalTransport sends requests for the public instance to an address
// inside the cluster, keeping the public Host and saying the hop was https,
// so nothing goes out through the edge and back.
type internalTransport struct {
	public, internal *url.URL
	next             http.RoundTripper
}

func (t *internalTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if t.internal == nil || r.URL.Host != t.public.Host {
		return t.next.RoundTrip(r)
	}
	r = r.Clone(r.Context())
	r.Host = t.public.Host
	r.URL.Scheme, r.URL.Host = t.internal.Scheme, t.internal.Host
	r.Header.Set("X-Forwarded-Proto", "https")
	return t.next.RoundTrip(r)
}
