package guard

import (
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/http"
	"strings"

	"github.com/myrouter/myrouter/internal/store"
)

// Identity resolves the client IP and device fingerprint for a request. It is
// deliberately conservative: X-Forwarded-For is only honoured when the direct
// peer is a configured trusted proxy, which prevents spoofing (PRD §7, §12.7).
type Identity struct {
	trustedProxies []*net.IPNet
	trustAll       bool
}

// NewIdentity builds an Identity from a trusted proxy list (CIDR or IP).
func NewIdentity(cidrs []string) *Identity {
	id := &Identity{}
	for _, c := range cidrs {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		if c == "*" {
			id.trustAll = true
			continue
		}
		if !strings.Contains(c, "/") {
			if strings.Contains(c, ":") {
				c += "/128"
			} else {
				c += "/32"
			}
		}
		_, ipnet, err := net.ParseCIDR(c)
		if err == nil {
			id.trustedProxies = append(id.trustedProxies, ipnet)
		}
	}
	return id
}

// ClientIP returns the effective client IP.
func (id *Identity) ClientIP(r *http.Request) string {
	peer := peerIP(r.RemoteAddr)
	if peer == "" {
		return ""
	}
	if !id.isTrusted(peer) {
		// Ignore forwarding headers entirely: the peer is the client.
		return peer
	}
	// Walk X-Forwarded-For right-to-left, stopping at the first untrusted hop.
	xff := r.Header.Get("X-Forwarded-For")
	if xff != "" {
		parts := strings.Split(xff, ",")
		for i := len(parts) - 1; i >= 0; i-- {
			ip := strings.TrimSpace(parts[i])
			if net.ParseIP(ip) == nil {
				continue
			}
			if !id.isTrusted(ip) {
				return ip
			}
		}
		// Every hop trusted: use the left-most entry.
		if ip := strings.TrimSpace(parts[0]); net.ParseIP(ip) != nil {
			return ip
		}
	}
	if ip := strings.TrimSpace(r.Header.Get("X-Real-IP")); net.ParseIP(ip) != nil {
		return ip
	}
	return peer
}

func (id *Identity) isTrusted(ipStr string) bool {
	if id.trustAll {
		return true
	}
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return false
	}
	for _, n := range id.trustedProxies {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

func peerIP(remoteAddr string) string {
	if remoteAddr == "" {
		return ""
	}
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return remoteAddr
	}
	return host
}

// Fingerprint derives a stable, non-reversible device identifier from the API
// key, client IP, User-Agent and an optional client-supplied X-Device-Id.
// This is a *soft* identifier, not an authentication guarantee.
func Fingerprint(apiKeyID, ip, userAgent, deviceHeader string) string {
	h := sha256.New()
	h.Write([]byte(strings.ToLower(strings.TrimSpace(apiKeyID))))
	h.Write([]byte("|"))
	if deviceHeader != "" {
		// An explicit client identifier narrows the fingerprint to the client.
		h.Write([]byte("dev:" + strings.TrimSpace(deviceHeader)))
	} else {
		h.Write([]byte(strings.TrimSpace(ip)))
		h.Write([]byte("|"))
		h.Write([]byte(strings.TrimSpace(userAgent)))
	}
	return hex.EncodeToString(h.Sum(nil))[:32]
}

// TrustedProxiesFromSettings reads the trusted proxy list from settings.
func TrustedProxiesFromSettings(db *store.DB) []string {
	v, err := db.GetSetting("trusted_proxies")
	if err != nil || strings.TrimSpace(v) == "" {
		return nil
	}
	return store.SplitCSV(v)
}
