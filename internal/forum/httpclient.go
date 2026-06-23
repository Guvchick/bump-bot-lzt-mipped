package forum

import (
	"fmt"
	"net/http"
	"net/url"
	"time"

	"golang.org/x/net/proxy"
)

// DefaultUserAgent is a realistic, stable desktop UA. Never send an empty UA or
// the Go default "Go-http-client" — XenForo and Cloudflare frown on those.
const DefaultUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:128.0) Gecko/20100101 Firefox/128.0"

// newTransport builds an *http.Transport, optionally routed through a proxy.
// Supported schemes: socks5/socks5h, http, https. Empty proxyURL = direct.
func newTransport(proxyURL string) (*http.Transport, error) {
	tr := &http.Transport{
		MaxIdleConns:        20,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 15 * time.Second,
		ForceAttemptHTTP2:   true,
	}
	if proxyURL == "" {
		return tr, nil
	}
	u, err := url.Parse(proxyURL)
	if err != nil {
		return nil, fmt.Errorf("parse proxy url: %w", err)
	}
	switch u.Scheme {
	case "socks5", "socks5h":
		var auth *proxy.Auth
		if u.User != nil {
			pw, _ := u.User.Password()
			auth = &proxy.Auth{User: u.User.Username(), Password: pw}
		}
		d, err := proxy.SOCKS5("tcp", u.Host, auth, proxy.Direct)
		if err != nil {
			return nil, fmt.Errorf("socks5 dialer: %w", err)
		}
		if cd, ok := d.(proxy.ContextDialer); ok {
			tr.DialContext = cd.DialContext
		}
	case "http", "https":
		tr.Proxy = http.ProxyURL(u)
	default:
		return nil, fmt.Errorf("unsupported proxy scheme %q", u.Scheme)
	}
	return tr, nil
}

// newClient returns an *http.Client with the given transport and timeout.
// jar may be nil (lolz needs no cookies; mipped passes a cookiejar).
func newClient(proxyURL string, jar http.CookieJar, timeout time.Duration) (*http.Client, error) {
	tr, err := newTransport(proxyURL)
	if err != nil {
		return nil, err
	}
	return &http.Client{Transport: tr, Jar: jar, Timeout: timeout}, nil
}
