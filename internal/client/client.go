// Package client implements the public EZGH Cloud HTTP conventions.
package client

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var DefaultEndpoints = map[string]string{
	"orgs": "https://orgs.ezghcloud.com", "compute": "https://compute-api.ezghcloud.com",
	"network": "https://network-api.ezghcloud.com", "firewall": "https://firewall-api.ezghcloud.com",
	"blockstore": "https://blockstore-api.ezghcloud.com", "images": "https://images-api.ezghcloud.com",
	"databases": "https://databases-api.ezghcloud.com", "files": "https://files-api.ezghcloud.com",
}

type Config struct {
	Token, OrganizationID, Version string
	Endpoints                      map[string]string
	HTTPClient                     *http.Client
}
type Client struct {
	OrganizationID string
	token, version string
	endpoints      map[string]string
	http           *http.Client
}
type Response struct {
	Body       []byte
	Header     http.Header
	StatusCode int
}

func (r *Response) Decode(target any) error {
	if len(bytes.TrimSpace(r.Body)) == 0 {
		return errors.New("API returned an empty JSON response")
	}
	return json.Unmarshal(r.Body, target)
}

type Error struct {
	StatusCode               int
	Code, Message, RequestID string
	Permission               string
	// Details are the error object's other fields (issues, reasons, restorableRanges), raw.
	// Error() leaves them and Message out; a service that trusts its own text adds them.
	Details map[string]json.RawMessage
}

func (e *Error) Error() string {
	message := fmt.Sprintf("EZGH Cloud API: HTTP %d (%s), request ID %s", e.StatusCode, e.Code, e.RequestID)
	if e.Permission != "" {
		message += "; required permission " + e.Permission
	}
	return message
}

// OperationError is an operation that ended failed or cancelled, with its error's code. Error()
// leaves Message out, as Error does; a service that trusts its own text adds it.
type OperationError struct{ Status, Code, Message string }

func (e *OperationError) Error() string {
	if e.Code == "" {
		return "operation ended with status " + e.Status + "; refresh the resource for details"
	}
	return "operation ended with status " + e.Status + " (" + e.Code + ")"
}

var permissionMessage = regexp.MustCompile(`(?:allowed to |permission[: ]+)([a-z][a-zA-Z0-9]*(?:\.[a-zA-Z][a-zA-Z0-9]*)+)`)

func New(cfg Config) *Client {
	endpoints := make(map[string]string, len(DefaultEndpoints))
	for k, v := range DefaultEndpoints {
		endpoints[k] = v
	}
	for k, v := range cfg.Endpoints {
		endpoints[k] = strings.TrimRight(v, "/")
	}
	hc := &http.Client{Timeout: 60 * time.Second}
	if cfg.HTTPClient != nil {
		*hc = *cfg.HTTPClient
	}
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{OrganizationID: cfg.OrganizationID, token: cfg.Token, version: cfg.Version, endpoints: endpoints, http: hc}
}

func OrgPath(org, collection string, ids ...string) string {
	p := "/v1/organizations/" + url.PathEscape(org) + "/" + url.PathEscape(collection)
	for _, id := range ids {
		p += "/" + url.PathEscape(id)
	}
	return p
}
func ProjectPath(org, project, collection string, ids ...string) string {
	p := OrgPath(org, "projects", project) + "/" + url.PathEscape(collection)
	for _, id := range ids {
		p += "/" + url.PathEscape(id)
	}
	return p
}

func (c *Client) Do(ctx context.Context, service, method, path string, body any) (*Response, error) {
	return c.DoWithHeaders(ctx, service, method, path, body, nil)
}
func (c *Client) DoWithHeaders(ctx context.Context, service, method, path string, body any, headers http.Header) (*Response, error) {
	base, ok := c.endpoints[service]
	if !ok {
		return nil, fmt.Errorf("unknown API service %q", service)
	}
	u, err := url.Parse(path)
	if err != nil || u.IsAbs() || u.Host != "" || !strings.HasPrefix(path, "/") {
		return nil, errors.New("API path must be an absolute path on the configured service")
	}
	var payload []byte
	if body != nil {
		payload, err = json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("encode API request: %w", err)
		}
	}
	var nonce [16]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	key := hex.EncodeToString(nonce[:])
	for attempt := 0; attempt < 4; attempt++ {
		req, err := http.NewRequestWithContext(ctx, method, base+path, bytes.NewReader(payload))
		if err != nil {
			return nil, errors.New("invalid API endpoint configuration")
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", "terraform-provider-ezghcloud/"+c.version)
		if c.token != "" {
			req.Header.Set("Authorization", "Bearer "+c.token)
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if method == http.MethodPost {
			req.Header.Set("Idempotency-Key", key)
		}
		req.Header.Set("X-Request-Id", key)
		for k, values := range headers {
			if strings.EqualFold(k, "Authorization") || strings.EqualFold(k, "Host") {
				return nil, errors.New("cannot override protected API headers")
			}
			req.Header[k] = values
		}
		raw, err := c.http.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, errors.New("API request failed before a response was received; refresh state before retrying")
		}
		data, readErr := io.ReadAll(io.LimitReader(raw.Body, 16*1024*1024+1))
		raw.Body.Close()
		if readErr != nil {
			return nil, errors.New("failed reading API response; refresh state before retrying")
		}
		if len(data) > 16*1024*1024 {
			return nil, errors.New("API response exceeds 16 MiB")
		}
		resp := &Response{Body: data, Header: raw.Header, StatusCode: raw.StatusCode}
		if raw.StatusCode >= 200 && raw.StatusCode < 300 {
			return resp, nil
		}
		apiErr := &Error{StatusCode: raw.StatusCode, Code: http.StatusText(raw.StatusCode), RequestID: raw.Header.Get("X-Request-Id")}
		var envelope struct {
			Error struct {
				Code, Message string `json:",omitempty"`
			} `json:"error"`
		}
		if json.Unmarshal(data, &envelope) == nil && envelope.Error.Code != "" {
			apiErr.Code = envelope.Error.Code
			apiErr.Message = envelope.Error.Message
			var raw struct{ Error map[string]json.RawMessage }
			if json.Unmarshal(data, &raw) == nil {
				delete(raw.Error, "code")
				delete(raw.Error, "message")
				apiErr.Details = raw.Error
			}
			if apiErr.Code == "access_denied" {
				if match := permissionMessage.FindStringSubmatch(apiErr.Message); len(match) == 2 {
					apiErr.Permission = match[1]
				}
			}
		}
		safe := method == http.MethodGet || method == http.MethodHead || method == http.MethodPost
		if !safe || attempt == 3 || (raw.StatusCode != 429 && raw.StatusCode != 503) || apiErr.Code == "quota_exceeded" {
			return nil, apiErr
		}
		delay := retryDelay(raw.Header, time.Duration(attempt+1)*time.Second)
		if delay > time.Minute {
			return nil, apiErr
		}
		if err := sleep(ctx, delay); err != nil {
			return nil, err
		}
	}
	return nil, errors.New("API retry limit reached")
}

func retryDelay(h http.Header, fallback time.Duration) time.Duration {
	if v := h.Get("Retry-After"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 32); err == nil && n >= 0 {
			return time.Duration(n) * time.Second
		}
		if t, err := http.ParseTime(v); err == nil {
			d := time.Until(t)
			if d < 0 {
				return 0
			}
			return d
		}
	}
	return fallback
}
func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (c *Client) List(ctx context.Context, service, path string) ([]map[string]any, error) {
	var rows []map[string]any
	seen := map[string]bool{}
	for {
		resp, err := c.Do(ctx, service, http.MethodGet, path, nil)
		if err != nil {
			return nil, err
		}
		var page struct {
			Items      []map[string]any `json:"items"`
			NextCursor *string          `json:"nextCursor"`
		}
		if err = resp.Decode(&page); err != nil {
			return nil, err
		}
		var metadata map[string]json.RawMessage
		if err = resp.Decode(&metadata); err != nil {
			return nil, err
		}
		if _, ok := metadata["nextCursor"]; !ok {
			return nil, errors.New("API list response omitted nextCursor")
		}
		if page.Items == nil {
			var envelope map[string]json.RawMessage
			if err = resp.Decode(&envelope); err != nil {
				return nil, err
			}
			u, _ := url.Parse(path)
			collection := u.Path[strings.LastIndex(u.Path, "/")+1:]
			parts := strings.Split(collection, "-")
			for i := 1; i < len(parts); i++ {
				if parts[i] != "" {
					parts[i] = strings.ToUpper(parts[i][:1]) + parts[i][1:]
				}
			}
			key := strings.Join(parts, "")
			data, ok := envelope[key]
			if !ok {
				return nil, fmt.Errorf("API list response omitted %s collection", key)
			}
			if err = json.Unmarshal(data, &page.Items); err != nil || page.Items == nil {
				return nil, fmt.Errorf("API list response has invalid %s collection", key)
			}
		}
		rows = append(rows, page.Items...)
		if page.NextCursor == nil || *page.NextCursor == "" {
			return rows, nil
		}
		cursor := *page.NextCursor
		if seen[cursor] {
			return nil, errors.New("API list repeated a pagination cursor")
		}
		seen[cursor] = true
		u, err := url.Parse(path)
		if err != nil {
			return nil, err
		}
		q := u.Query()
		q.Set("cursor", cursor)
		u.RawQuery = q.Encode()
		path = u.String()
	}
}

func (c *Client) Wait(ctx context.Context, service, location string) error {
	base, ok := c.endpoints[service]
	if !ok {
		return errors.New("unknown operation service")
	}
	u, err := url.Parse(location)
	if err != nil {
		return errors.New("invalid operation location")
	}
	if u.IsAbs() {
		b, _ := url.Parse(base)
		if u.Scheme != b.Scheme || u.Host != b.Host || u.User != nil {
			return errors.New("operation location has a different origin")
		}
		u.Scheme = ""
		u.Host = ""
	}
	if !strings.HasPrefix(u.Path, "/v1/") && !strings.HasPrefix(u.Path, "/operations/") {
		return errors.New("invalid operation location path")
	}
	for {
		resp, err := c.Do(ctx, service, http.MethodGet, u.String(), nil)
		if err != nil {
			return err
		}
		var out map[string]any
		if err = resp.Decode(&out); err != nil {
			return err
		}
		if op, ok := out["operation"].(map[string]any); ok {
			out = op
		}
		status, _ := out["status"].(string)
		switch status {
		case "succeeded":
			return nil
		case "failed", "cancelled", "canceled":
			opErr := &OperationError{Status: status}
			if e, _ := out["error"].(map[string]any); e != nil {
				opErr.Code, _ = e["code"].(string)
				opErr.Message, _ = e["message"].(string)
			}
			return opErr
		case "pending", "queued", "running", "in_progress":
		default:
			return fmt.Errorf("API returned unknown operation status %q", status)
		}
		if err = sleep(ctx, retryDelay(resp.Header, time.Second)); err != nil {
			return err
		}
	}
}
