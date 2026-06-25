package forum

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// LolzBaseURL is the official Lolzteam REST API base. Token is obtained at
// https://lolz.live/account/api and sent as "Authorization: Bearer <token>".
const LolzBaseURL = "https://prod-api.lolz.live"

// Lolz is the Lolzteam API client. It implements Forum.
type Lolz struct {
	baseURL string
	timeout time.Duration
}

var _ Forum = (*Lolz)(nil)

// NewLolz builds a Lolz client.
func NewLolz() *Lolz {
	return &Lolz{baseURL: LolzBaseURL, timeout: 30 * time.Second}
}

// Bump raises the thread via POST /threads/{id}/bump.
func (c *Lolz) Bump(ctx context.Context, acc Account, t Thread) (BumpResult, error) {
	body, status, err := c.do(ctx, acc, http.MethodPost, "/threads/"+t.Ref+"/bump", nil)
	if err != nil {
		return BumpResult{}, err
	}
	if status == http.StatusUnauthorized {
		return BumpResult{}, ErrAuthFailed
	}
	if msgs, isErr := parseLolzErrors(body); isErr {
		msg := strings.Join(msgs, "; ")
		if isAuthMessage(msg) {
			return BumpResult{}, ErrAuthFailed
		}
		if d, ok := ParseWaitDuration(msg); ok {
			return BumpResult{OK: false, Message: msg, RetryAfter: d}, nil
		}
		// A non-time error (e.g. "thread is not bumpable yet" without a number) —
		// soft failure; the scheduler will retry at the configured interval.
		return BumpResult{OK: false, Message: msg}, nil
	}
	if status >= 400 {
		return BumpResult{}, fmt.Errorf("lolz bump http %d: %s", status, truncate(body, 200))
	}
	return BumpResult{OK: true, Message: "bumped"}, nil
}

// ThreadStats fetches GET /threads/{id} and extracts title/views/replies.
func (c *Lolz) ThreadStats(ctx context.Context, acc Account, t Thread) (ThreadStats, error) {
	body, status, err := c.do(ctx, acc, http.MethodGet, "/threads/"+t.Ref, nil)
	if err != nil {
		return ThreadStats{}, err
	}
	if status == http.StatusUnauthorized {
		return ThreadStats{}, ErrAuthFailed
	}
	if status >= 400 {
		hint := ""
		if status == http.StatusForbidden {
			hint = " (токену, вероятно, не хватает scope «read»)"
		}
		if msgs, isErr := parseLolzErrors(body); isErr {
			return ThreadStats{}, fmt.Errorf("lolz thread http %d: %s%s", status, strings.Join(msgs, "; "), hint)
		}
		return ThreadStats{}, fmt.Errorf("lolz thread http %d%s", status, hint)
	}

	var resp lolzThreadResp
	if err := json.Unmarshal(body, &resp); err != nil {
		return ThreadStats{}, fmt.Errorf("decode thread json: %w", err)
	}
	th := resp.Thread
	if th == nil {
		// Some responses return the thread object at top level.
		var top lolzThread
		if json.Unmarshal(body, &top) == nil {
			th = &top
		}
	}
	if th == nil {
		return ThreadStats{}, fmt.Errorf("lolz thread: no thread in response")
	}

	stats := ThreadStats{Title: firstNonEmpty(th.ThreadTitle, th.Title)}
	stats.Views = firstNonNil(th.ThreadViewCount, th.ViewCount, th.Views)
	if r := firstNonNil(th.ThreadReplyCount, th.ReplyCount); r != nil {
		stats.Replies = r
	} else if pc := firstNonNil(th.ThreadPostCount, th.PostCount); pc != nil {
		// post_count includes the opening post; replies = posts - 1.
		v := *pc - 1
		if v < 0 {
			v = 0
		}
		stats.Replies = &v
	}
	return stats, nil
}

// CheckAuth verifies the token via GET /users/me.
func (c *Lolz) CheckAuth(ctx context.Context, acc Account) error {
	_, status, err := c.do(ctx, acc, http.MethodGet, "/users/me", nil)
	if err != nil {
		return err
	}
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return ErrAuthFailed
	}
	if status >= 400 {
		return fmt.Errorf("lolz checkauth http %d", status)
	}
	return nil
}

var _ ThreadLister = (*Lolz)(nil)

// MyThreads enumerates threads created by the authenticated user via
// GET /users/me (for the id) then paginated GET /threads?creator_user_id=.
// Requires the token's "read" scope.
func (c *Lolz) MyThreads(ctx context.Context, acc Account) ([]DiscoveredThread, error) {
	uid, err := c.userID(ctx, acc)
	if err != nil {
		return nil, err
	}

	var out []DiscoveredThread
	seen := make(map[string]bool)
	for page := 1; page <= 50; page++ {
		path := fmt.Sprintf("/threads?creator_user_id=%s&page=%d", url.QueryEscape(uid), page)
		body, status, err := c.do(ctx, acc, http.MethodGet, path, nil)
		if err != nil {
			return out, err
		}
		if status == http.StatusUnauthorized || status == http.StatusForbidden {
			if len(out) > 0 {
				return out, nil
			}
			return nil, fmt.Errorf("%w (или токену не хватает scope «read»)", ErrAuthFailed)
		}
		if status >= 400 {
			break
		}
		var r lolzThreadsResp
		if json.Unmarshal(body, &r) != nil || len(r.Threads) == 0 {
			break
		}
		for _, th := range r.Threads {
			ref := firstNonEmpty(th.ThreadID.String(), th.ID.String())
			if ref == "" || ref == "0" || seen[ref] {
				continue
			}
			seen[ref] = true
			out = append(out, DiscoveredThread{Ref: ref, Title: firstNonEmpty(th.ThreadTitle, th.Title)})
		}
		// Stop when pagination info says we're on the last page; if there's no
		// pagination info, stop after the first page to avoid an unbounded loop.
		if r.Links.Pages > 0 {
			if page >= r.Links.Pages {
				break
			}
		} else if r.Links.Next == "" {
			break
		}
	}
	return out, nil
}

// userID resolves the authenticated user's numeric id from GET /users/me.
func (c *Lolz) userID(ctx context.Context, acc Account) (string, error) {
	body, status, err := c.do(ctx, acc, http.MethodGet, "/users/me", nil)
	if err != nil {
		return "", err
	}
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return "", ErrAuthFailed
	}
	if status >= 400 {
		return "", fmt.Errorf("lolz users/me http %d", status)
	}
	var r struct {
		User struct {
			UserID json.Number `json:"user_id"`
			ID     json.Number `json:"id"`
		} `json:"user"`
		UserID json.Number `json:"user_id"`
	}
	_ = json.Unmarshal(body, &r)
	if id := firstNonEmpty(r.User.UserID.String(), r.User.ID.String(), r.UserID.String()); id != "" {
		return id, nil
	}
	return "", fmt.Errorf("не удалось определить user_id из /users/me")
}

// do performs an authenticated request and returns body, status, and a non-nil
// error only on transport failures.
func (c *Lolz) do(ctx context.Context, acc Account, method, path string, body io.Reader) ([]byte, int, error) {
	client, err := newClient(acc.Proxy, nil, c.timeout)
	if err != nil {
		return nil, 0, err
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(acc.Secret)))
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", DefaultUserAgent)

	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return b, resp.StatusCode, nil
}

// lolzThread mirrors the XenForo thread object; field names appear with and
// without the "thread_" prefix depending on the endpoint, so we accept both.
type lolzThread struct {
	ThreadTitle      string `json:"thread_title"`
	Title            string `json:"title"`
	ThreadViewCount  *int   `json:"thread_view_count"`
	ViewCount        *int   `json:"view_count"`
	Views            *int   `json:"views"`
	ThreadPostCount  *int   `json:"thread_post_count"`
	PostCount        *int   `json:"post_count"`
	ThreadReplyCount *int   `json:"thread_reply_count"`
	ReplyCount       *int   `json:"reply_count"`
}

type lolzThreadResp struct {
	Thread *lolzThread `json:"thread"`
}

// lolzListThread is a row from GET /threads (lighter than lolzThread).
type lolzListThread struct {
	ThreadID    json.Number `json:"thread_id"`
	ID          json.Number `json:"id"`
	ThreadTitle string      `json:"thread_title"`
	Title       string      `json:"title"`
}

type lolzThreadsResp struct {
	Threads []lolzListThread `json:"threads"`
	Links   struct {
		Pages int    `json:"pages"`
		Next  string `json:"next"`
	} `json:"links"`
}

// parseLolzErrors extracts XenForo-style error messages. Handles `errors` as an
// array of strings or objects, and a string `error`.
func parseLolzErrors(body []byte) ([]string, bool) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, false
	}
	var out []string
	if raw, ok := m["errors"]; ok {
		out = append(out, extractMessages(raw)...)
	}
	if raw, ok := m["error"]; ok {
		out = append(out, extractMessages(raw)...)
	}
	if raw, ok := m["error_description"]; ok {
		out = append(out, extractMessages(raw)...)
	}
	return out, len(out) > 0
}

func extractMessages(raw json.RawMessage) []string {
	var arr []string
	if json.Unmarshal(raw, &arr) == nil && len(arr) > 0 {
		return arr
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if s != "" {
			return []string{s}
		}
		return nil
	}
	var objs []map[string]any
	if json.Unmarshal(raw, &objs) == nil {
		var res []string
		for _, o := range objs {
			if msg, ok := o["message"].(string); ok && msg != "" {
				res = append(res, msg)
			}
		}
		return res
	}
	var obj map[string]any
	if json.Unmarshal(raw, &obj) == nil {
		if msg, ok := obj["message"].(string); ok && msg != "" {
			return []string{msg}
		}
	}
	return nil
}

func isAuthMessage(msg string) bool {
	l := strings.ToLower(msg)
	for _, kw := range []string{"token", "unauthor", "не авториз", "войдите", "invalid_token", "access"} {
		if strings.Contains(l, kw) {
			return true
		}
	}
	return false
}

// ---- small shared utilities ----

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func firstNonNil(vals ...*int) *int {
	for _, v := range vals {
		if v != nil {
			return v
		}
	}
	return nil
}

func truncate(b []byte, n int) string {
	s := strings.TrimSpace(string(b))
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// bodyReader is a tiny helper to build a JSON body reader.
func bodyReader(v any) (io.Reader, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return bytes.NewReader(b), nil
}
