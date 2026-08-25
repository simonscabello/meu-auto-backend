package httpx

import (
	"net"
	"net/http"
	"strings"
)

// ClientIP returns the caller's address for rate limiting.
//
// Behind a proxy, r.RemoteAddr is the proxy — on Railway that means every request in the
// world shares one address, and a per-IP limit would lock out all users at once. So when
// the deployment sits behind a trusted proxy, the leftmost X-Forwarded-For entry is used
// instead.
//
// trustProxy must be false when the process is reachable directly. X-Forwarded-For is
// client-supplied, and trusting it without a proxy in front lets anyone send a fresh fake
// address on every request and bypass the limit entirely.
func ClientIP(r *http.Request, trustProxy bool) string {
	if trustProxy {
		if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
			// Leftmost entry is the original client; the rest were appended by each hop.
			first, _, _ := strings.Cut(forwarded, ",")
			if ip := net.ParseIP(strings.TrimSpace(first)); ip != nil {
				return ip.String()
			}
		}
		if real := strings.TrimSpace(r.Header.Get("X-Real-Ip")); real != "" {
			if ip := net.ParseIP(real); ip != nil {
				return ip.String()
			}
		}
	}

	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		// RemoteAddr is not always host:port (a unix socket, for instance).
		return r.RemoteAddr
	}
	return host
}
