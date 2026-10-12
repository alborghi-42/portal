package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"path"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is projects.yaml.
type Config struct {
	Login    string           `yaml:"login"`
	Cursus   int              `yaml:"cursus"`
	Hide     []string         `yaml:"hide"`
	Projects map[string]Extra `yaml:"projects"`
}

// Extra is what the YAML adds on top of the intra.
type Extra struct {
	Title       string   `yaml:"title"`
	Slug        string   `yaml:"slug"` // intra slug when it differs from the key
	Description string   `yaml:"description"`
	Stack       []string `yaml:"stack"`
	Repo        string   `yaml:"repo"`
	Screenshot  string   `yaml:"screenshot"`
	Port        int      `yaml:"port"`
}

type Project struct {
	Key string
	Extra
	Status    string // finished | in_progress | ...
	Mark      *int
	Validated bool
	MarkedAt  *time.Time
	StartedAt time.Time
	Live      bool
}

func (p Project) HasApp() bool { return p.Port > 0 }

func (p Project) StatusLabel() string {
	switch {
	case p.Status == "finished" && p.Validated:
		return "Validated"
	case p.Status == "finished":
		return "Failed"
	case p.Status == "" || p.Status == "in_progress" || p.Status == "searching_a_group" || p.Status == "creating_group" || p.Status == "waiting_for_correction":
		return "In progress"
	}
	return strings.ReplaceAll(p.Status, "_", " ")
}

func (p Project) MarkText() string {
	if p.Mark == nil || p.Status != "finished" {
		return ""
	}
	return strconv.Itoa(*p.Mark)
}

// Catalog merges the intra and projects.yaml and keeps the last good copy of each, so
// a failed refresh never empties the site.
type Catalog struct {
	intra       *Intra
	projectsURL string
	appHost     string

	mu    sync.RWMutex
	cfg   Config
	users []ProjectUser
}

func NewCatalog(intra *Intra, projectsURL, appHost string, fallback []byte) (*Catalog, error) {
	c := &Catalog{intra: intra, projectsURL: projectsURL, appHost: appHost}
	if err := yaml.Unmarshal(fallback, &c.cfg); err != nil {
		return nil, fmt.Errorf("embedded projects.yaml: %w", err)
	}
	return c, nil
}

// Run refreshes the YAML every 10 minutes and the intra every 30, until ctx ends.
func (c *Catalog) Run(ctx context.Context) {
	c.refreshConfig(ctx)
	c.refreshIntra(ctx)
	cfgTick := time.NewTicker(10 * time.Minute)
	intraTick := time.NewTicker(30 * time.Minute)
	defer cfgTick.Stop()
	defer intraTick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-cfgTick.C:
			c.refreshConfig(ctx)
		case <-intraTick.C:
			c.refreshIntra(ctx)
		}
	}
}

func (c *Catalog) refreshConfig(ctx context.Context) {
	if c.projectsURL == "" {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.projectsURL, nil)
	if err != nil {
		log.Printf("projects.yaml: %v", err)
		return
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Printf("projects.yaml: %v", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		log.Printf("projects.yaml: %s", resp.Status)
		return
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		log.Printf("projects.yaml: %v", err)
		return
	}
	var cfg Config
	if err := yaml.Unmarshal(body, &cfg); err != nil {
		log.Printf("projects.yaml: keeping previous copy: %v", err)
		return
	}
	c.mu.Lock()
	c.cfg = cfg
	c.mu.Unlock()
}

func (c *Catalog) refreshIntra(ctx context.Context) {
	if c.intra == nil {
		return
	}
	c.mu.RLock()
	login := c.cfg.Login
	c.mu.RUnlock()
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	users, err := c.intra.ProjectsUsers(ctx, login)
	if err != nil {
		log.Printf("intra: keeping previous copy: %v", err)
		return
	}
	c.mu.Lock()
	c.users = users
	c.mu.Unlock()
	log.Printf("intra: %d projects_users for %s", len(users), login)
}

// Projects returns the merged list: in progress first, then most recently marked.
func (c *Catalog) Projects() []Project {
	c.mu.RLock()
	cfg, users := c.cfg, c.users
	c.mu.RUnlock()

	bySlug := map[string]string{} // intra slug -> key
	for key, x := range cfg.Projects {
		bySlug[cmpOr(x.Slug, key)] = key
	}

	seen := map[string]bool{}
	var out []Project
	for _, u := range users {
		if u.Project.ParentID != nil || !slices.Contains(u.CursusIDs, cfg.Cursus) || hidden(cfg.Hide, u.Project.Slug) {
			continue
		}
		key, ok := bySlug[u.Project.Slug]
		if !ok {
			key = strings.TrimPrefix(u.Project.Slug, "42cursus-")
		}
		x := cfg.Projects[key]
		x.Title = cmpOr(x.Title, u.Project.Name)
		p := Project{
			Key: key, Extra: x, Status: u.Status, Mark: u.FinalMark,
			Validated: u.Validated != nil && *u.Validated, MarkedAt: u.MarkedAt, StartedAt: u.CreatedAt,
		}
		seen[key] = true
		out = append(out, p)
	}
	// Projects the intra does not know about yet are work in progress by definition.
	for key, x := range cfg.Projects {
		if !seen[key] {
			x.Title = cmpOr(x.Title, key)
			out = append(out, Project{Key: key, Extra: x})
		}
	}

	c.probe(out)
	slices.SortStableFunc(out, func(a, b Project) int {
		ai, bi := a.StatusLabel() == "In progress", b.StatusLabel() == "In progress"
		if ai != bi {
			if ai {
				return -1
			}
			return 1
		}
		return sortTime(b).Compare(sortTime(a))
	})
	return out
}

func (c *Catalog) Project(key string) (Project, bool) {
	for _, p := range c.Projects() {
		if p.Key == key {
			return p, true
		}
	}
	return Project{}, false
}

// Port is the app port of a project, 0 when it has none. Cheap: no intra, no probing.
func (c *Catalog) Port(key string) int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.cfg.Projects[key].Port
}

// probe marks a project Live when something accepts connections on its port.
func (c *Catalog) probe(ps []Project) {
	if c.appHost == "" {
		return
	}
	var wg sync.WaitGroup
	for i := range ps {
		if !ps[i].HasApp() {
			continue
		}
		wg.Add(1)
		go func(p *Project) {
			defer wg.Done()
			conn, err := net.DialTimeout("tcp", net.JoinHostPort(c.appHost, strconv.Itoa(p.Port)), 400*time.Millisecond)
			if err == nil {
				conn.Close()
				p.Live = true
			}
		}(&ps[i])
	}
	wg.Wait()
}

func sortTime(p Project) time.Time {
	if p.MarkedAt != nil {
		return *p.MarkedAt
	}
	return p.StartedAt
}

func hidden(patterns []string, slug string) bool {
	for _, pat := range patterns {
		if ok, _ := path.Match(pat, slug); ok {
			return true
		}
	}
	return false
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
