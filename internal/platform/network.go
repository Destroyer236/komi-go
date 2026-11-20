package platform

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// HTTPClient builds the shared transport. An empty proxy means "follow the
// system proxy"; an explicit value overrides it. SOCKS5 is intentionally not
// supported here because github.com does not ship a socks dialer.
func HTTPClient(proxyRaw string) (*http.Client, error) {
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return &http.Client{Timeout: 0}, nil
	}
	tr := base.Clone()
	tr.ResponseHeaderTimeout = 20 * time.Second
	tr.TLSHandshakeTimeout = 15 * time.Second

	proxyRaw = strings.TrimSpace(proxyRaw)
	if proxyRaw == "" {
		tr.Proxy = http.ProxyFromEnvironment
		return &http.Client{Transport: tr}, nil
	}
	u, err := url.Parse(proxyRaw)
	if err != nil || u.Host == "" {
		return nil, errors.New("代理地址格式应为 http://host:port")
	}
	switch u.Scheme {
	case "http", "https":
	default:
		return nil, errors.New("只支持 http/https 代理")
	}
	if u.User != nil && u.User.String() == "" {
		return nil, errors.New("代理凭据不完整")
	}
	tr.Proxy = http.ProxyURL(u)
	return &http.Client{Transport: tr}, nil
}
