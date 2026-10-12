package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const intraAPI = "https://api.intra.42.fr"

// Cloudflare in front of the intra answers 403 to clients without a User-Agent.
const userAgent = "42-portal (+https://42.bombatomica64.dev)"

// Intra is a client_credentials client for the 42 API. It only needs public data
// (a user's projects), so the app's own token is enough.
type Intra struct {
	uid, secret string
	http        *http.Client

	mu      sync.Mutex
	token   string
	expires time.Time
}

func NewIntra(uid, secret string) *Intra {
	return &Intra{uid: uid, secret: secret, http: &http.Client{Timeout: 20 * time.Second}}
}

type ProjectUser struct {
	Status    string     `json:"status"`
	FinalMark *int       `json:"final_mark"`
	Validated *bool      `json:"validated?"`
	MarkedAt  *time.Time `json:"marked_at"`
	CreatedAt time.Time  `json:"created_at"`
	CursusIDs []int      `json:"cursus_ids"`
	Project   struct {
		Name     string `json:"name"`
		Slug     string `json:"slug"`
		ParentID *int   `json:"parent_id"`
	} `json:"project"`
}

func (c *Intra) accessToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && time.Now().Before(c.expires) {
		return c.token, nil
	}
	form := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {c.uid},
		"client_secret": {c.secret},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, intraAPI+"/oauth/token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", userAgent)
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("intra token: %s", resp.Status)
	}
	var tok struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tok); err != nil {
		return "", err
	}
	c.token = tok.AccessToken
	// Renew a minute early rather than racing the expiry mid-request.
	c.expires = time.Now().Add(time.Duration(tok.ExpiresIn)*time.Second - time.Minute)
	return c.token, nil
}

// ProjectsUsers returns every project the user is or was registered to, all pages.
func (c *Intra) ProjectsUsers(ctx context.Context, login string) ([]ProjectUser, error) {
	tok, err := c.accessToken(ctx)
	if err != nil {
		return nil, err
	}
	var all []ProjectUser
	for page := 1; page <= 20; page++ {
		u := fmt.Sprintf("%s/v2/users/%s/projects_users?page[size]=100&page[number]=%d", intraAPI, url.PathEscape(login), page)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("User-Agent", userAgent)
		resp, err := c.http.Do(req)
		if err != nil {
			return nil, err
		}
		var batch []ProjectUser
		if resp.StatusCode == http.StatusOK {
			err = json.NewDecoder(resp.Body).Decode(&batch)
		} else {
			err = fmt.Errorf("intra projects_users page %d: %s", page, resp.Status)
		}
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		all = append(all, batch...)
		if len(batch) < 100 {
			break
		}
	}
	return all, nil
}
