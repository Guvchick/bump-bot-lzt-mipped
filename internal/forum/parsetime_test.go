package forum

import (
	"encoding/json"
	"testing"
	"time"
)

func TestParseWaitDuration(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want time.Duration
		ok   bool
	}{
		{
			name: "mipped russian full",
			in:   "Нужно подождать 0 дней, 23 часов, 57 минут и 38 секунд чтобы поднять тему",
			want: 23*time.Hour + 57*time.Minute + 38*time.Second,
			ok:   true,
		},
		{
			name: "russian hours minutes",
			in:   "Подождите 4 часа и 12 минут",
			want: 4*time.Hour + 12*time.Minute,
			ok:   true,
		},
		{
			name: "english",
			in:   "You must wait 2 hours and 5 minutes",
			want: 2*time.Hour + 5*time.Minute,
			ok:   true,
		},
		{
			name: "days",
			in:   "1 день 3 часа",
			want: 24*time.Hour + 3*time.Hour,
			ok:   true,
		},
		{
			name: "no duration",
			in:   "Тема не найдена",
			want: 0,
			ok:   false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ParseWaitDuration(tc.in)
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v", ok, tc.ok)
			}
			if ok && got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestParseMippedRef(t *testing.T) {
	cases := []struct {
		in       string
		wantSlug string
		wantID   string
		wantErr  bool
	}{
		{"https://mipped.com/f/threads/some-cool-title.123456/", "some-cool-title", "123456", false},
		{"https://mipped.com/f/threads/title.999/", "title", "999", false},
		{"/f/threads/abc-def.42", "abc-def", "42", false},
		{"some-slug.777", "some-slug", "777", false},
		{"not-a-thread", "", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			slug, id, err := parseMippedRef(tc.in)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if tc.wantErr {
				return
			}
			if slug != tc.wantSlug || id != tc.wantID {
				t.Fatalf("got (%q,%q), want (%q,%q)", slug, id, tc.wantSlug, tc.wantID)
			}
		})
	}
}

func TestCookiesFromHeader(t *testing.T) {
	// Values can contain '=' (base64) and there can be stray spaces.
	blob, n := CookiesFromHeader("xf_user=123,abc==; xf_session=deadBEEF==;  xf_csrf=99,ff ")
	if n != 3 {
		t.Fatalf("n = %d, want 3", n)
	}
	var cs []storedCookie
	if err := json.Unmarshal(blob, &cs); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, c := range cs {
		got[c.Name] = c.Value
	}
	if got["xf_user"] != "123,abc==" || got["xf_session"] != "deadBEEF==" || got["xf_csrf"] != "99,ff" {
		t.Fatalf("unexpected cookies: %+v", got)
	}

	if _, n := CookiesFromHeader("   ;  ; garbage"); n != 0 {
		t.Fatalf("expected 0 cookies from junk, got %d", n)
	}
}

func TestExtractCSRF(t *testing.T) {
	cases := []struct {
		name string
		html string
		want string
	}{
		{"data-csrf", `<html data-csrf="1700000000,abcdef">`, "1700000000,abcdef"},
		{"hidden input", `<form><input type="hidden" name="_xfToken" value="1700000000,deadbeef"></form>`, "1700000000,deadbeef"},
		{"js config", `<script>XF.config.csrf = '1700000000,cafef00d';</script>`, "1700000000,cafef00d"},
		{"json csrf", `<script>{"csrf":"1700000000,0badf00d"}</script>`, "1700000000,0badf00d"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := extractCSRF([]byte(tc.html)); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}
