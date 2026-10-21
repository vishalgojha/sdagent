// Package gmail provides direct Gmail API access using standard Google OAuth.
package gmail

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/vishalgojha/sdsheetal/internal/config"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	authURL  = "https://accounts.google.com/o/oauth2/v2/auth"
	tokenURL = "https://oauth2.googleapis.com/token"
	apiBase  = "https://gmail.googleapis.com/gmail/v1/users/me"
	scopes   = "https://www.googleapis.com/auth/gmail.modify"
)

type Client struct {
	cfg   *config.Config
	http  *http.Client
	mu    sync.Mutex
	token tokenFile
	state string
}
type tokenFile struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	TokenType    string    `json:"token_type"`
	Expiry       time.Time `json:"expiry"`
	Email        string    `json:"email"`
}
type Connection struct {
	ConnectionID string `json:"connection_id"`
	Provider     string `json:"provider"`
	UserID       string `json:"user_id"`
	UpdatedAt    string `json:"updated_at"`
}
type Message struct {
	ID       string `json:"id"`
	ThreadID string `json:"thread_id"`
	From     string `json:"from"`
	Subject  string `json:"subject"`
	Date     string `json:"date"`
	Snippet  string `json:"snippet"`
	Unread   bool   `json:"unread"`
}

func New(cfg *config.Config) *Client {
	c := &Client{cfg: cfg, http: &http.Client{Timeout: 18 * time.Second}}
	_ = c.load()
	return c
}
func (c *Client) tokenPath() string { return filepath.Join(c.cfg.DataDir, "gmail-token.json") }
func (c *Client) load() error {
	b, e := os.ReadFile(c.tokenPath())
	if e != nil {
		return e
	}
	return json.Unmarshal(b, &c.token)
}
func (c *Client) save() error {
	if e := os.MkdirAll(c.cfg.DataDir, 0700); e != nil {
		return e
	}
	b, e := json.MarshalIndent(c.token, "", "  ")
	if e != nil {
		return e
	}
	return os.WriteFile(c.tokenPath(), b, 0600)
}
func (c *Client) Enabled() bool { return c.cfg.HasGoogleOAuth() }
func (c *Client) Connection() *Connection {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token.RefreshToken == "" {
		return nil
	}
	return &Connection{ConnectionID: "google", Provider: "gmail", UserID: c.token.Email, UpdatedAt: time.Now().UTC().Format(time.RFC3339)}
}
func (c *Client) SetConnection(_ *Connection) {}

func (c *Client) ConnectLink() (string, error) {
	if !c.Enabled() {
		return "", fmt.Errorf("Google OAuth is not configured")
	}
	b := make([]byte, 18)
	if _, e := rand.Read(b); e != nil {
		return "", e
	}
	c.mu.Lock()
	c.state = hex.EncodeToString(b)
	state := c.state
	c.mu.Unlock()
	v := url.Values{"client_id": {c.cfg.GoogleClientID}, "redirect_uri": {c.cfg.GoogleRedirectURL}, "response_type": {"code"}, "scope": {scopes}, "access_type": {"offline"}, "prompt": {"consent"}, "state": {state}}
	return authURL + "?" + v.Encode(), nil
}
func (c *Client) Callback(code, state string) error {
	c.mu.Lock()
	valid := state != "" && state == c.state
	oldRefresh := c.token.RefreshToken
	c.mu.Unlock()
	if !valid {
		return fmt.Errorf("invalid or expired Google OAuth state")
	}
	f := url.Values{"code": {code}, "client_id": {c.cfg.GoogleClientID}, "client_secret": {c.cfg.GoogleClientSecret}, "redirect_uri": {c.cfg.GoogleRedirectURL}, "grant_type": {"authorization_code"}}
	var t struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		TokenType    string `json:"token_type"`
		ExpiresIn    int    `json:"expires_in"`
	}
	if e := c.postForm(tokenURL, f, &t); e != nil {
		return e
	}
	if t.AccessToken == "" {
		return fmt.Errorf("Google did not return an access token")
	}
	if t.RefreshToken == "" {
		t.RefreshToken = oldRefresh
	}
	c.mu.Lock()
	c.token = tokenFile{AccessToken: t.AccessToken, RefreshToken: t.RefreshToken, TokenType: t.TokenType, Expiry: time.Now().Add(time.Duration(t.ExpiresIn) * time.Second)}
	if e := c.save(); e != nil {
		c.mu.Unlock()
		return e
	}
	c.mu.Unlock()
	_, _ = c.profileLocked()
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.save()
}
func (c *Client) postForm(endpoint string, form url.Values, out any) error {
	r, e := c.http.PostForm(endpoint, form)
	if e != nil {
		return e
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	if r.StatusCode < 200 || r.StatusCode > 299 {
		return fmt.Errorf("Google OAuth: %s", snippet(string(b)))
	}
	return json.Unmarshal(b, out)
}
func (c *Client) Disconnect() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.token = tokenFile{}
	e := os.Remove(c.tokenPath())
	if os.IsNotExist(e) {
		return nil
	}
	return e
}
func (c *Client) accessToken() (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token.RefreshToken == "" {
		return "", fmt.Errorf("no Gmail connection found; connect Google from the web panel")
	}
	if c.token.AccessToken != "" && time.Now().Before(c.token.Expiry.Add(-60*time.Second)) {
		return c.token.AccessToken, nil
	}
	f := url.Values{"client_id": {c.cfg.GoogleClientID}, "client_secret": {c.cfg.GoogleClientSecret}, "refresh_token": {c.token.RefreshToken}, "grant_type": {"refresh_token"}}
	var t struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if e := c.postForm(tokenURL, f, &t); e != nil {
		return "", e
	}
	c.token.AccessToken = t.AccessToken
	c.token.TokenType = t.TokenType
	c.token.Expiry = time.Now().Add(time.Duration(t.ExpiresIn) * time.Second)
	_ = c.save()
	return c.token.AccessToken, nil
}
func (c *Client) request(method, endpoint string, payload, out any) error {
	tok, e := c.accessToken()
	if e != nil {
		return e
	}
	var body io.Reader
	if payload != nil {
		b, _ := json.Marshal(payload)
		body = bytes.NewReader(b)
	}
	q, e := http.NewRequest(method, endpoint, body)
	if e != nil {
		return e
	}
	q.Header.Set("Authorization", "Bearer "+tok)
	q.Header.Set("Accept", "application/json")
	if payload != nil {
		q.Header.Set("Content-Type", "application/json")
	}
	r, e := c.http.Do(q)
	if e != nil {
		return e
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	if r.StatusCode < 200 || r.StatusCode > 299 {
		return fmt.Errorf("Gmail API: %s", snippet(string(b)))
	}
	if out != nil && len(b) > 0 {
		return json.Unmarshal(b, out)
	}
	return nil
}
func (c *Client) profileLocked() (string, error) {
	var p struct {
		Email string `json:"emailAddress"`
	}
	e := c.request(http.MethodGet, apiBase+"/profile", nil, &p)
	if e == nil {
		c.token.Email = p.Email
	}
	return p.Email, e
}
func (c *Client) ProfileEmail() string {
	c.mu.Lock()
	email := c.token.Email
	c.mu.Unlock()
	if email == "" {
		email, _ = c.profileLocked()
	}
	return email
}

func (c *Client) Messages(query string, limit int) ([]Message, error) {
	if limit <= 0 || limit > 20 {
		limit = 8
	}
	v := url.Values{"maxResults": {fmt.Sprint(limit)}}
	if strings.TrimSpace(query) != "" {
		v.Set("q", query)
	}
	var l struct {
		Messages []struct {
			ID string `json:"id"`
		} `json:"messages"`
	}
	if e := c.request(http.MethodGet, apiBase+"/messages?"+v.Encode(), nil, &l); e != nil {
		return nil, e
	}
	out := make([]Message, 0, len(l.Messages))
	for _, x := range l.Messages {
		var m struct {
			ID       string   `json:"id"`
			ThreadID string   `json:"threadId"`
			Snippet  string   `json:"snippet"`
			LabelIDs []string `json:"labelIds"`
			Payload  struct {
				Headers []struct {
					Name  string `json:"name"`
					Value string `json:"value"`
				} `json:"headers"`
			} `json:"payload"`
		}
		q := url.Values{"format": {"metadata"}, "metadataHeaders": {"From", "Subject", "Date"}}
		if c.request(http.MethodGet, apiBase+"/messages/"+url.PathEscape(x.ID)+"?"+q.Encode(), nil, &m) != nil {
			continue
		}
		h := map[string]string{}
		for _, z := range m.Payload.Headers {
			h[strings.ToLower(z.Name)] = z.Value
		}
		unread := false
		for _, z := range m.LabelIDs {
			if z == "UNREAD" {
				unread = true
			}
		}
		out = append(out, Message{ID: m.ID, ThreadID: m.ThreadID, From: h["from"], Subject: firstNonEmpty(h["subject"], "(no subject)"), Date: h["date"], Snippet: m.Snippet, Unread: unread})
	}
	return out, nil
}
func (c *Client) SearchIDs(query string, limit int) ([]string, error) {
	if limit <= 0 || limit > 10000 {
		limit = 1000
	}
	ids := []string{}
	page := ""
	for len(ids) < limit {
		v := url.Values{"maxResults": {fmt.Sprint(min(limit-len(ids), 500))}}
		if query != "" {
			v.Set("q", query)
		}
		if page != "" {
			v.Set("pageToken", page)
		}
		var p struct {
			Messages []struct {
				ID string `json:"id"`
			} `json:"messages"`
			Next string `json:"nextPageToken"`
		}
		if e := c.request(http.MethodGet, apiBase+"/messages?"+v.Encode(), nil, &p); e != nil {
			return nil, e
		}
		for _, m := range p.Messages {
			if len(ids) < limit {
				ids = append(ids, m.ID)
			}
		}
		if p.Next == "" || len(p.Messages) == 0 {
			break
		}
		page = p.Next
	}
	return ids, nil
}
func (c *Client) BatchModify(ids, add, remove []string) error {
	for i := 0; i < len(ids); i += 1000 {
		e := min(i+1000, len(ids))
		if x := c.request(http.MethodPost, apiBase+"/messages/batchModify", map[string]any{"ids": ids[i:e], "addLabelIds": add, "removeLabelIds": remove}, nil); x != nil {
			return x
		}
	}
	return nil
}
func (c *Client) TrashQuery(q string, n int) (int, error) {
	ids, e := c.SearchIDs(q, n)
	if e != nil {
		return 0, e
	}
	return len(ids), c.BatchModify(ids, []string{"TRASH"}, []string{"INBOX"})
}
func (c *Client) RestoreQuery(q string, n int) (int, error) {
	ids, e := c.SearchIDs(strings.TrimSpace(q)+" in:trash", n)
	if e != nil {
		return 0, e
	}
	return len(ids), c.BatchModify(ids, nil, []string{"TRASH"})
}
func firstNonEmpty(v ...string) string {
	for _, x := range v {
		if strings.TrimSpace(x) != "" {
			return x
		}
	}
	return ""
}
func snippet(s string) string {
	r := []rune(s)
	if len(r) > 220 {
		return string(r[:220])
	}
	return s
}
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
