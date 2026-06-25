package forum

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
)

// mippedBase is the Mipped (XenForo) site root.
const mippedBase = "https://mipped.com"

// errNeedLogin is an internal sentinel: the session is missing/expired and a
// fresh login is required before retrying.
var errNeedLogin = errors.New("mipped: login required")

// csrfRe pulls the rotating _xfToken (format "unixtime,hash") out of inline JS.
var csrfRe = regexp.MustCompile(`(?:"csrf"\s*:\s*"|XF\.config\.csrf\s*=\s*['"])([^"']+)`)

// mippedRefRe parses a Mipped thread reference into slug + numeric id. It accepts
// a full URL, a "/f/threads/..." path, or a bare "slug.12345".
var mippedRefRe = regexp.MustCompile(`(?:threads/)?([^/?#]+?)\.(\d+)/?(?:[?#].*)?$`)

// mippedHrefRe pulls slug+id out of any thread href (anywhere in the string),
// e.g. "/f/threads/some-title.12345/" or ".../12345/post-99".
var mippedHrefRe = regexp.MustCompile(`/threads/([^/?#"]+?)\.(\d+)`)

// mippedMemberRe pulls the numeric member id out of a "/members/name.123/" link.
var mippedMemberRe = regexp.MustCompile(`/members/[^/?#"]*?\.(\d+)`)

// Mipped drives the Mipped forum through a logged-in browser-like session.
type Mipped struct {
	timeout time.Duration
}

var _ Forum = (*Mipped)(nil)

// NewMipped builds a Mipped client.
func NewMipped() *Mipped {
	return &Mipped{timeout: 30 * time.Second}
}

type mippedCreds struct {
	Login    string `json:"login"`
	Password string `json:"password"`
}

type storedCookie struct {
	Name  string `json:"n"`
	Value string `json:"v"`
}

type mippedUpResp struct {
	Status string          `json:"status"`
	Errors json.RawMessage `json:"errors"`
}

// Bump performs the thread page -> CSRF -> POST /up flow, re-logging in once if
// the session has expired.
func (c *Mipped) Bump(ctx context.Context, acc Account, t Thread) (BumpResult, error) {
	slug, id, err := parseMippedRef(t.Ref)
	if err != nil {
		return BumpResult{}, err
	}
	client, jar, err := c.newSession(acc)
	if err != nil {
		return BumpResult{}, err
	}
	creds, err := parseCreds(acc.Secret)
	if err != nil {
		return BumpResult{}, err
	}

	res, err := c.bumpOnce(ctx, client, slug, id)
	if errors.Is(err, errNeedLogin) {
		if lerr := c.login(ctx, client, jar, creds); lerr != nil {
			return BumpResult{}, lerr
		}
		c.persist(ctx, acc, jar)
		res, err = c.bumpOnce(ctx, client, slug, id)
		if errors.Is(err, errNeedLogin) {
			return BumpResult{}, ErrAuthFailed
		}
	}
	if err == nil {
		c.persist(ctx, acc, jar) // cookies (xf_csrf) may have rotated
	}
	return res, err
}

// bumpOnce does a single GET-page -> POST-/up cycle. errNeedLogin means caller
// should log in and retry.
func (c *Mipped) bumpOnce(ctx context.Context, client *http.Client, slug, id string) (BumpResult, error) {
	threadURL := mippedBase + "/f/threads/" + slug + "." + id + "/"
	body, finalURL, _, err := c.get(ctx, client, threadURL)
	if err != nil {
		return BumpResult{}, err
	}
	if isLoginRedirect(finalURL) || isLoginHTML(body) {
		return BumpResult{}, errNeedLogin
	}
	token := extractCSRF(body)
	if token == "" {
		return BumpResult{}, errNeedLogin
	}

	requestURI := "/f/threads/" + slug + "." + id + "/"
	payload := map[string]any{
		"_xfRequestUri":   requestURI,
		"_xfResponseType": "json",
		"_xfToken":        token,
		"_xfWithData":     1,
	}
	br, err := bodyReader(payload)
	if err != nil {
		return BumpResult{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, threadURL+"up", br)
	if err != nil {
		return BumpResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("Origin", mippedBase)
	req.Header.Set("Referer", threadURL)
	req.Header.Set("User-Agent", DefaultUserAgent)
	req.Header.Set("Accept", "application/json, text/javascript, */*; q=0.01")

	resp, err := client.Do(req)
	if err != nil {
		return BumpResult{}, err
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	// Note: a "too early" answer arrives as HTTP 403 *with JSON*, which is normal —
	// parse the body regardless of status; only an HTML body means trouble.
	if looksHTML(rb) {
		if isLoginHTML(rb) {
			return BumpResult{}, errNeedLogin
		}
		return BumpResult{}, fmt.Errorf("mipped up: unexpected HTML response (http %d)", resp.StatusCode)
	}

	var up mippedUpResp
	if err := json.Unmarshal(rb, &up); err != nil {
		return BumpResult{}, fmt.Errorf("mipped up: decode json (http %d): %w", resp.StatusCode, err)
	}
	if strings.EqualFold(up.Status, "ok") {
		return BumpResult{OK: true, Message: "bumped"}, nil
	}
	msg := strings.Join(extractMessages(up.Errors), "; ")
	if msg == "" {
		msg = "bump rejected"
	}
	if isAuthMessage(msg) {
		return BumpResult{}, errNeedLogin
	}
	if d, ok := ParseWaitDuration(msg); ok {
		return BumpResult{OK: false, Message: msg, RetryAfter: d}, nil
	}
	return BumpResult{OK: false, Message: msg}, nil
}

// ThreadStats scrapes the thread page (best effort; missing fields stay nil).
func (c *Mipped) ThreadStats(ctx context.Context, acc Account, t Thread) (ThreadStats, error) {
	slug, id, err := parseMippedRef(t.Ref)
	if err != nil {
		return ThreadStats{}, err
	}
	client, jar, err := c.newSession(acc)
	if err != nil {
		return ThreadStats{}, err
	}
	threadURL := mippedBase + "/f/threads/" + slug + "." + id + "/"
	body, finalURL, _, err := c.get(ctx, client, threadURL)
	if err != nil {
		return ThreadStats{}, err
	}
	if isLoginRedirect(finalURL) || isLoginHTML(body) {
		creds, cerr := parseCreds(acc.Secret)
		if cerr == nil && c.login(ctx, client, jar, creds) == nil {
			c.persist(ctx, acc, jar)
			body, _, _, _ = c.get(ctx, client, threadURL)
		}
	}
	return parseMippedStats(body), nil
}

// CheckAuth verifies the session, logging in if necessary.
func (c *Mipped) CheckAuth(ctx context.Context, acc Account) error {
	client, jar, err := c.newSession(acc)
	if err != nil {
		return err
	}
	creds, err := parseCreds(acc.Secret)
	if err != nil {
		return err
	}
	if c.sessionAlive(ctx, client) {
		return nil
	}
	if err := c.login(ctx, client, jar, creds); err != nil {
		return err
	}
	c.persist(ctx, acc, jar)
	return nil
}

var _ ThreadLister = (*Mipped)(nil)

// MyThreads scrapes the member's "recent content" pages for their threads.
// Mipped has no public API, so this is best effort and may need selector tweaks
// if the forum theme differs.
func (c *Mipped) MyThreads(ctx context.Context, acc Account) ([]DiscoveredThread, error) {
	client, jar, err := c.newSession(acc)
	if err != nil {
		return nil, err
	}
	body, finalURL, _, err := c.get(ctx, client, mippedBase+"/f/account/")
	if err != nil {
		return nil, err
	}
	if isLoginRedirect(finalURL) || isLoginHTML(body) {
		creds, _ := parseCreds(acc.Secret)
		if lerr := c.login(ctx, client, jar, creds); lerr != nil {
			return nil, lerr
		}
		c.persist(ctx, acc, jar)
		body, _, _, _ = c.get(ctx, client, mippedBase+"/f/account/")
	}

	memberID := extractMemberID(body)
	if memberID == "" {
		return nil, fmt.Errorf("не удалось определить ID пользователя Mipped")
	}

	var out []DiscoveredThread
	seen := make(map[string]bool)
	for page := 1; page <= 20; page++ {
		u := fmt.Sprintf("%s/f/members/%s/recent-content?type=thread&page=%d", mippedBase, memberID, page)
		pb, _, status, err := c.get(ctx, client, u)
		if err != nil {
			return out, err
		}
		if status >= 400 {
			break
		}
		if scrapeThreadLinks(pb, seen, &out) == 0 {
			break
		}
	}
	return out, nil
}

// extractMemberID finds the logged-in user's numeric member id on a page.
func extractMemberID(body []byte) string {
	if doc, err := goquery.NewDocumentFromReader(bytes.NewReader(body)); err == nil {
		for _, sel := range []string{"a.p-navgroup-link--user", ".p-navgroup--member a[href*='/members/']"} {
			if href, ok := doc.Find(sel).First().Attr("href"); ok {
				if m := mippedMemberRe.FindStringSubmatch(href); m != nil {
					return m[1]
				}
			}
		}
	}
	if m := mippedMemberRe.FindSubmatch(body); m != nil {
		return string(m[1])
	}
	return ""
}

// scrapeThreadLinks collects new thread links from a recent-content page,
// returning how many were added.
func scrapeThreadLinks(body []byte, seen map[string]bool, out *[]DiscoveredThread) int {
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(body))
	if err != nil {
		return 0
	}
	sel := doc.Find(".contentRow-title a[href*='/threads/'], .structItem-title a[href*='/threads/']")
	if sel.Length() == 0 {
		sel = doc.Find("a[href*='/threads/']")
	}
	added := 0
	sel.Each(func(_ int, s *goquery.Selection) {
		href, _ := s.Attr("href")
		m := mippedHrefRe.FindStringSubmatch(href)
		if m == nil {
			return
		}
		slug, id := m[1], m[2]
		if seen[id] {
			return
		}
		title := strings.TrimSpace(s.Text())
		if len([]rune(title)) < 2 { // skip count/icon links
			return
		}
		seen[id] = true
		*out = append(*out, DiscoveredThread{Ref: mippedBase + "/f/threads/" + slug + "." + id + "/", Title: title})
		added++
	})
	return added
}

// login fetches the login page for a fresh CSRF token, then posts credentials.
// A cookie-only account (no stored login/password) cannot log in — typically
// because the login form is CAPTCHA-gated — so it returns ErrAuthFailed asking
// for fresh cookies.
func (c *Mipped) login(ctx context.Context, client *http.Client, jar *cookiejar.Jar, creds mippedCreds) error {
	if creds.Login == "" || creds.Password == "" {
		return fmt.Errorf("%w: сессия недействительна, обновите cookies (логин по паролю недоступен)", ErrAuthFailed)
	}
	body, _, _, err := c.get(ctx, client, mippedBase+"/f/login/")
	if err != nil {
		return err
	}
	token := extractCSRF(body)

	form := url.Values{}
	form.Set("login", creds.Login)
	form.Set("password", creds.Password)
	form.Set("remember", "1")
	form.Set("_xfRedirect", mippedBase+"/f/")
	form.Set("_xfToken", token)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, mippedBase+"/f/login/login", strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", DefaultUserAgent)
	req.Header.Set("Origin", mippedBase)
	req.Header.Set("Referer", mippedBase+"/f/login/")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	rb, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()

	if hasCookie(jar, "xf_user") {
		return nil
	}
	msg := extractLoginError(rb)
	if msg == "" {
		msg = "login failed (check credentials)"
	}
	if lc := strings.ToLower(msg); strings.Contains(lc, "captcha") || strings.Contains(lc, "капч") {
		msg += " → используйте 🍪 Cookies в карточке аккаунта"
	}
	return fmt.Errorf("%w: %s", ErrAuthFailed, msg)
}

// sessionAlive checks the account page is reachable without a login redirect.
func (c *Mipped) sessionAlive(ctx context.Context, client *http.Client) bool {
	body, finalURL, status, err := c.get(ctx, client, mippedBase+"/f/account/")
	if err != nil || status >= 400 {
		return false
	}
	return !isLoginRedirect(finalURL) && !isLoginHTML(body)
}

// newSession builds an http.Client with a cookiejar seeded from acc.Session.
func (c *Mipped) newSession(acc Account) (*http.Client, *cookiejar.Jar, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, nil, err
	}
	if len(acc.Session) > 0 {
		var cs []storedCookie
		if json.Unmarshal(acc.Session, &cs) == nil && len(cs) > 0 {
			cookies := make([]*http.Cookie, 0, len(cs))
			for _, sc := range cs {
				cookies = append(cookies, &http.Cookie{Name: sc.Name, Value: sc.Value, Path: "/"})
			}
			if u, perr := url.Parse(mippedBase + "/"); perr == nil {
				jar.SetCookies(u, cookies)
			}
		}
	}
	client, err := newClient(acc.Proxy, jar, c.timeout)
	if err != nil {
		return nil, nil, err
	}
	return client, jar, nil
}

// persist serialises the current cookies back to storage via acc.SaveSession.
func (c *Mipped) persist(ctx context.Context, acc Account, jar *cookiejar.Jar) {
	if acc.SaveSession == nil {
		return
	}
	u, err := url.Parse(mippedBase + "/")
	if err != nil {
		return
	}
	var cs []storedCookie
	for _, ck := range jar.Cookies(u) {
		cs = append(cs, storedCookie{Name: ck.Name, Value: ck.Value})
	}
	if len(cs) == 0 {
		return
	}
	if b, err := json.Marshal(cs); err == nil {
		_ = acc.SaveSession(ctx, b)
	}
}

// get issues a browser-like GET and returns the body, final URL (after
// redirects), and status.
func (c *Mipped) get(ctx context.Context, client *http.Client, rawURL string) ([]byte, *url.URL, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, nil, 0, err
	}
	req.Header.Set("User-Agent", DefaultUserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "ru-RU,ru;q=0.9,en;q=0.8")

	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, 0, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, resp.Request.URL, resp.StatusCode, err
	}
	return b, resp.Request.URL, resp.StatusCode, nil
}

// ---- parsing helpers ----

func parseMippedRef(ref string) (slug, id string, err error) {
	m := mippedRefRe.FindStringSubmatch(strings.TrimSpace(ref))
	if m == nil {
		return "", "", fmt.Errorf("cannot parse mipped thread ref %q (expected a thread URL or slug.id)", ref)
	}
	return m[1], m[2], nil
}

func parseCreds(secret []byte) (mippedCreds, error) {
	var creds mippedCreds
	if len(secret) == 0 {
		return creds, nil // cookie-only account: no login/password stored
	}
	if err := json.Unmarshal(secret, &creds); err != nil {
		return creds, fmt.Errorf("decode mipped credentials: %w", err)
	}
	// Empty login/password is allowed — such an account works via cookies only;
	// login() reports a clear auth error if it is ever actually needed.
	return creds, nil
}

// CookiesFromHeader parses a browser cookie string ("name=value; name2=value2")
// into the session blob the Mipped client stores/reuses. Returns the JSON and
// the number of cookies parsed. Use it to add/refresh a Mipped session when the
// login form is gated by a CAPTCHA.
func CookiesFromHeader(header string) ([]byte, int) {
	var cs []storedCookie
	for _, part := range strings.Split(header, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		eq := strings.IndexByte(part, '=')
		if eq <= 0 {
			continue
		}
		name := strings.TrimSpace(part[:eq])
		val := strings.TrimSpace(part[eq+1:])
		if name == "" {
			continue
		}
		cs = append(cs, storedCookie{Name: name, Value: val})
	}
	if len(cs) == 0 {
		return nil, 0
	}
	b, _ := json.Marshal(cs)
	return b, len(cs)
}

func extractCSRF(body []byte) string {
	if doc, err := goquery.NewDocumentFromReader(bytes.NewReader(body)); err == nil {
		if v, ok := doc.Find("html").Attr("data-csrf"); ok && v != "" {
			return v
		}
		if v, ok := doc.Find("input[name='_xfToken']").First().Attr("value"); ok && v != "" {
			return v
		}
	}
	if m := csrfRe.FindSubmatch(body); m != nil {
		return string(m[1])
	}
	return ""
}

func parseMippedStats(body []byte) ThreadStats {
	var st ThreadStats
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(body))
	if err != nil {
		return st
	}
	st.Title = strings.TrimSpace(doc.Find("h1.p-title-value").First().Text())
	if st.Title == "" {
		if v, ok := doc.Find("meta[property='og:title']").Attr("content"); ok {
			st.Title = strings.TrimSpace(v)
		}
	}
	// XenForo stats live in <dl class="pairs"> blocks; labels vary by locale.
	doc.Find("dl.pairs").Each(func(_ int, s *goquery.Selection) {
		label := strings.ToLower(strings.TrimSpace(s.Find("dt").Text()))
		n, ok := parseIntLoose(s.Find("dd").Text())
		if !ok {
			return
		}
		switch {
		case strings.Contains(label, "просмотр") || strings.Contains(label, "view"):
			v := n
			st.Views = &v
		case strings.Contains(label, "ответ") || strings.Contains(label, "repl"):
			v := n
			st.Replies = &v
		}
	})
	return st
}

func extractLoginError(body []byte) string {
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(body))
	if err != nil {
		return ""
	}
	for _, sel := range []string{".blockMessage--error", ".formSubmitRow-text", ".blockMessage"} {
		if txt := strings.TrimSpace(doc.Find(sel).First().Text()); txt != "" {
			return txt
		}
	}
	return ""
}

func parseIntLoose(s string) (int, bool) {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return 0, false
	}
	n := 0
	for _, r := range b.String() {
		n = n*10 + int(r-'0')
	}
	return n, true
}

func hasCookie(jar *cookiejar.Jar, name string) bool {
	u, err := url.Parse(mippedBase + "/")
	if err != nil {
		return false
	}
	for _, ck := range jar.Cookies(u) {
		if ck.Name == name && ck.Value != "" {
			return true
		}
	}
	return false
}

func looksHTML(b []byte) bool {
	t := strings.TrimSpace(string(b))
	return strings.HasPrefix(t, "<")
}

func isLoginRedirect(u *url.URL) bool {
	return u != nil && strings.Contains(u.Path, "/login")
}

func isLoginHTML(b []byte) bool {
	if !looksHTML(b) {
		return false
	}
	s := string(b)
	if strings.Contains(s, `data-template="login"`) {
		return true
	}
	return strings.Contains(s, `name="login"`) && strings.Contains(s, `name="password"`)
}
