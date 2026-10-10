package keycloak

import (
	"context"
	"net"
	"net/http"
	"time"
)

// HTTPClient is for every call to Keycloak: OIDC discovery and login, the
// service account token, the admin API. IPv4 only, because Keycloak's IPv6
// endpoint resets TLS connections ("connection reset by peer"), seen from the
// VPS and from developer machines alike. Short timeouts, so a hung Keycloak
// cannot hang a page. Otherwise it behaves like http.DefaultTransport
// (proxy from the environment, HTTP/2, idle connections closed after 90s).
var HTTPClient = &http.Client{
	Timeout: 5 * time.Second,
	Transport: &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			dialer := &net.Dialer{Timeout: 3 * time.Second}
			return dialer.DialContext(ctx, "tcp4", addr)
		},
		ForceAttemptHTTP2:     true,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   3 * time.Second,
		ResponseHeaderTimeout: 3 * time.Second,
	},
}
