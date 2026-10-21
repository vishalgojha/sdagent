// Package gmail provides read-only Gmail access through Nango's provider
// proxy, matching the original app's integration.
package gmail

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/vishalgojha/sdsheetal/internal/config"
)

type Client struct {
	cfg        *config.Config
	http       *http.Client
	connection *Connection
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
	return &Client{
		cfg: cfg,
		http: &http.Client{
			Timeout: 18 * time.Second,
		},
	}
}

func (c *Client) Enabled() bool { return c.cfg.HasNango() }

func (c *Client) SetConnection(conn *Connection) { c.connection = conn }

func (c *Client) Connection() *Connection { return c.connection }

// Disconnect removes the current Nango connection and forgets it locally.
func (c *Client) Disconnect() error {
	if c.connection == nil || c.connection.ConnectionID == "" {
		_, err := c.RefreshConnection()
		if err != nil {
			return err
		}
	}
	id := c.connection.ConnectionID
	// Nango requires the provider config key when deleting a connection.
	deletePath := "/connections/" + url.PathEscape(id) + "?provider_config_key=" + url.QueryEscape(c.cfg.NangoIntegration)
	if _, err := c.nangoRequest(http.MethodDelete, deletePath, nil, nil); err != nil {
		// Nango is already disconnected; make the operation idempotent.
		if !strings.Contains(strings.ToLower(err.Error()), "unknown_connection") {
			return err
		}
	}
	c.connection = nil
	return nil
}

func (c *Client) nangoRequest(method, path string, payload any, headers map[string]string) ([]byte, error) {
	if c.cfg.NangoSecretKey == "" {
		return nil, fmt.Errorf("Nango is not configured")
	}
	var raw []byte
	if payload != nil {
		raw, _ = json.Marshal(payload)
	}
	req, err := http.NewRequest(method, strings.TrimRight(c.cfg.NangoAPIBase, "/")+"/"+strings.TrimLeft(path, "/"), bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.cfg.NangoSecretKey)
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("nango %s: %s", resp.Status, snippet(string(body)))
	}
	return body, nil
}

func snippet(s string) string {
	runes := []rune(s)
	if len(runes) > 220 {
		return string(runes[:220])
	}
	return s
}

// ConnectLink creates a hosted Nango Connect session for Gmail.
func (c *Client) ConnectLink() (string, error) {
	body, err := c.nangoRequest("POST", "/connect/sessions", map[string]any{
		"tags": map[string]string{
			"end_user_id":           c.cfg.NangoUserID,
			"end_user_display_name": "Sheetal",
		},
		"allowed_integrations": []string{c.cfg.NangoIntegration},
	}, nil)
	if err != nil {
		return "", err
	}
	var parsed struct {
		Data struct {
			ConnectLink string `json:"connect_link"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", err
	}
	if parsed.Data.ConnectLink == "" {
		return "", fmt.Errorf("Nango did not return a connect link")
	}
	return parsed.Data.ConnectLink, nil
}

// RefreshConnection resolves the stored or first Gmail connection for the user.
func (c *Client) RefreshConnection() (string, error) {
	if c.connection != nil && c.connection.ConnectionID != "" {
		// Do not trust a stale configured ID forever; the list below validates it.
	}
	if c.cfg.NangoIntegration == "" {
		return "", fmt.Errorf("Nango integration not configured")
	}
	params := url.Values{}
	params.Set("provider_config_key", c.cfg.NangoIntegration)
	params.Set("limit", "20")
	body, err := c.nangoRequest("GET", "/connections?"+params.Encode(), nil, nil)
	if err != nil {
		return "", err
	}
	var parsed struct {
		Connections []struct {
			Provider_config_key string `json:"provider_config_key"`
			ProviderConfigKey   string `json:"providerConfigKey"`
			Connection_id       string `json:"connection_id"`
			ConnectionId        string `json:"connectionId"`
		} `json:"connections"`
		Data struct {
			Connections []struct {
				Provider_config_key string `json:"provider_config_key"`
				ProviderConfigKey   string `json:"providerConfigKey"`
				Connection_id       string `json:"connection_id"`
				ConnectionId        string `json:"connectionId"`
			} `json:"connections"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", err
	}
	connections := parsed.Connections
	if len(connections) == 0 {
		connections = parsed.Data.Connections
	}
	for _, conn := range connections {
		provider := firstNonEmpty(conn.Provider_config_key, conn.ProviderConfigKey)
		id := firstNonEmpty(conn.Connection_id, conn.ConnectionId)
		if provider == c.cfg.NangoIntegration && id != "" {
			c.connection = &Connection{
				ConnectionID: id, Provider: provider, UserID: c.cfg.NangoUserID,
				UpdatedAt: time.Now().UTC().Format(time.RFC3339),
			}
			return id, nil
		}
	}
	return "", fmt.Errorf("no Gmail connection found for this user")
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// Messages reads up to limit Gmail messages with metadata only.
func (c *Client) Messages(query string, limit int) ([]Message, error) {
	if limit <= 0 || limit > 20 {
		limit = 8
	}
	connID, err := c.RefreshConnection()
	if err != nil {
		return nil, err
	}
	headers := map[string]string{
		"Provider-Config-Key": c.cfg.NangoIntegration,
		"Connection-Id":       connID,
	}
	params := url.Values{}
	params.Set("maxResults", fmt.Sprint(limit))
	if q := strings.TrimSpace(query); q != "" {
		params.Set("q", q)
	}
	body, err := c.nangoRequest("GET", "/proxy/gmail/v1/users/me/messages?"+params.Encode(), nil, headers)
	if err != nil {
		return nil, err
	}
	var list struct {
		Messages []struct {
			ID string `json:"id"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, err
	}
	out := make([]Message, 0, len(list.Messages))
	for i, item := range list.Messages {
		if i >= limit {
			break
		}
		detail, err := c.nangoRequest("GET", "/proxy/gmail/v1/users/me/messages/"+url.PathEscape(item.ID)+"?"+url.Values{
			"format":          {"metadata"},
			"metadataHeaders": {"From", "Subject", "Date"},
		}.Encode(), nil, headers)
		if err != nil {
			continue
		}
		var message struct {
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
		if err := json.Unmarshal(detail, &message); err != nil {
			continue
		}
		headersMap := map[string]string{}
		for _, h := range message.Payload.Headers {
			headersMap[strings.ToLower(h.Name)] = h.Value
		}
		unread := false
		for _, l := range message.LabelIDs {
			if l == "UNREAD" {
				unread = true
				break
			}
		}
		subject := strings.TrimSpace(headersMap["subject"])
		if subject == "" {
			subject = "(no subject)"
		}
		out = append(out, Message{
			ID: message.ID, ThreadID: message.ThreadID,
			From: strings.TrimSpace(headersMap["from"]), Subject: subject,
			Date: strings.TrimSpace(headersMap["date"]), Snippet: message.Snippet,
			Unread: unread,
		})
	}
	return out, nil
}

// SearchIDs returns Gmail message IDs matching a query. It is intentionally
// bounded because cleanup actions must be previewable before mutation.
func (c *Client) SearchIDs(query string, limit int) ([]string, error) {
	if limit <= 0 || limit > 10000 {
		limit = 1000
	}
	connID, err := c.RefreshConnection()
	if err != nil {
		return nil, err
	}
	headers := map[string]string{"Provider-Config-Key": c.cfg.NangoIntegration, "Connection-Id": connID}
	ids := make([]string, 0, limit)
	pageToken := ""
	for len(ids) < limit {
		params := url.Values{"maxResults": []string{fmt.Sprint(min(limit-len(ids), 500))}}
		if strings.TrimSpace(query) != "" {
			params.Set("q", query)
		}
		if pageToken != "" {
			params.Set("pageToken", pageToken)
		}
		body, err := c.nangoRequest(http.MethodGet, "/proxy/gmail/v1/users/me/messages?"+params.Encode(), nil, headers)
		if err != nil {
			return nil, err
		}
		var page struct {
			Messages []struct {
				ID string `json:"id"`
			} `json:"messages"`
			NextPageToken string `json:"nextPageToken"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, err
		}
		for _, msg := range page.Messages {
			if msg.ID != "" && len(ids) < limit {
				ids = append(ids, msg.ID)
			}
		}
		if page.NextPageToken == "" || len(page.Messages) == 0 {
			break
		}
		pageToken = page.NextPageToken
	}
	return ids, nil
}

// BatchModify applies Gmail label mutations in API-sized chunks.
func (c *Client) BatchModify(ids, addLabelIDs, removeLabelIDs []string) error {
	if len(ids) == 0 {
		return nil
	}
	connID, err := c.RefreshConnection()
	if err != nil {
		return err
	}
	headers := map[string]string{"Provider-Config-Key": c.cfg.NangoIntegration, "Connection-Id": connID}
	for start := 0; start < len(ids); start += 1000 {
		end := start + 1000
		if end > len(ids) {
			end = len(ids)
		}
		_, err := c.nangoRequest(http.MethodPost, "/proxy/gmail/v1/users/me/messages/batchModify", map[string]any{
			"ids": ids[start:end], "addLabelIds": addLabelIDs, "removeLabelIds": removeLabelIDs,
		}, headers)
		if err != nil {
			return err
		}
	}
	return nil
}

// TrashQuery moves matching mail to Gmail Trash. Permanent deletion is not
// exposed by the native skill because it is irreversible.
func (c *Client) TrashQuery(query string, limit int) (int, error) {
	ids, err := c.SearchIDs(query, limit)
	if err != nil {
		return 0, err
	}
	if err := c.BatchModify(ids, []string{"TRASH"}, []string{"INBOX"}); err != nil {
		return 0, err
	}
	return len(ids), nil
}

// RestoreQuery removes TRASH from matching messages. It does not put them in
// the inbox automatically, matching the ClawHub skill's safer restore mode.
func (c *Client) RestoreQuery(query string, limit int) (int, error) {
	ids, err := c.SearchIDs(strings.TrimSpace(query)+" in:trash", limit)
	if err != nil {
		return 0, err
	}
	if err := c.BatchModify(ids, nil, []string{"TRASH"}); err != nil {
		return 0, err
	}
	return len(ids), nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
