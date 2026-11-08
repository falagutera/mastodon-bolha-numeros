package main

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// pages remembers who turned on their public page: account id and username,
// in a small JSON file on a volume. A public URL is only looked up here, so a
// name that isn't on the list costs a map lookup and never a query.
type pages struct {
	path string

	mu     sync.Mutex
	byID   map[int64]pageEntry
	byName map[string]int64 // lower-case username
}

type pageEntry struct {
	User  string    `json:"user"`
	Since time.Time `json:"since"`
}

func openPages(path string) (*pages, error) {
	p := &pages{path: path, byID: map[int64]pageEntry{}, byName: map[string]int64{}}
	raw, err := os.ReadFile(filepath.Clean(path)) // the operator's setting, never a visitor's input
	if errors.Is(err, fs.ErrNotExist) {
		return p, nil
	}
	if err != nil {
		return nil, err
	}
	var file map[string]pageEntry
	if err := json.Unmarshal(raw, &file); err != nil {
		return nil, err
	}
	for k, e := range file {
		id, err := strconv.ParseInt(k, 10, 64)
		if err != nil || e.User == "" {
			continue
		}
		p.byID[id] = e
		// Two ids with one name (someone renamed and another took the name):
		// the one who turned the page on last has it.
		k := strings.ToLower(e.User)
		if other, ok := p.byName[k]; !ok || p.byID[other].Since.Before(e.Since) {
			p.byName[k] = id
		}
	}
	return p, nil
}

// lookup: the account behind a public URL, if its page is on.
func (p *pages) lookup(user string) (int64, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	id, ok := p.byName[strings.ToLower(user)]
	return id, ok
}

func (p *pages) on(id int64) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, ok := p.byID[id]
	return ok
}

func (p *pages) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.byID)
}

// set turns a page on or off and writes the file.
func (p *pages) set(id int64, user string, on bool, now time.Time) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	old, had := p.byID[id]
	if had {
		p.dropName(old.User, id)
		delete(p.byID, id)
	}
	if on {
		p.byID[id] = pageEntry{User: user, Since: now.UTC()}
		p.byName[strings.ToLower(user)] = id
	}
	if err := p.save(); err != nil { // keep memory as the file says
		if on {
			p.dropName(user, id)
			delete(p.byID, id)
		}
		if had {
			p.byID[id] = old
			p.byName[strings.ToLower(old.User)] = id
		}
		return err
	}
	return nil
}

// rename follows an account that changed its username.
func (p *pages) rename(id int64, user string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	old, ok := p.byID[id]
	if !ok || old.User == user {
		return nil
	}
	p.dropName(old.User, id)
	old.User = user
	p.byID[id] = old
	p.byName[strings.ToLower(user)] = id
	return p.save()
}

// dropName removes a name from the index only if it still points at id: after a
// rename, someone else may have the name now.
func (p *pages) dropName(user string, id int64) {
	k := strings.ToLower(user)
	if p.byName[k] == id {
		delete(p.byName, k)
	}
}

// save writes the file atomically (the caller holds the lock).
func (p *pages) save() error {
	file := make(map[string]pageEntry, len(p.byID))
	for id, e := range p.byID {
		file[strconv.FormatInt(id, 10)] = e
	}
	raw, err := json.MarshalIndent(file, "", " ")
	if err != nil {
		return err
	}
	tmp := p.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Clean(p.path))
}
