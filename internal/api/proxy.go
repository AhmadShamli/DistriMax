package api

import (
	"net"
	"net/http"
	"strings"
)

// ResolveClientIP determines the actual client IP, respecting X-Forwarded-For / X-Real-IP
// only if the direct connecting socket IP is in the trusted proxy CIDR list.
func ResolveClientIP(r *http.Request, isTrustedProxy func(net.IP) bool) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	directIP := net.ParseIP(host)
	if directIP == nil {
		return host
	}

	// If direct socket is not a trusted proxy, always use direct IP
	if isTrustedProxy == nil || !isTrustedProxy(directIP) {
		return host
	}

	// Direct socket is trusted proxy: check X-Forwarded-For
	xff := r.Header.Get("X-Forwarded-For")
	if xff != "" {
		parts := strings.Split(xff, ",")
		for _, part := range parts {
			clientCandidate := strings.TrimSpace(part)
			ip := net.ParseIP(clientCandidate)
			if ip != nil {
				return clientCandidate
			}
		}
	}

	// Check X-Real-IP
	xri := strings.TrimSpace(r.Header.Get("X-Real-IP"))
	if xri != "" {
		if ip := net.ParseIP(xri); ip != nil {
			return xri
		}
	}

	return host
}
