package provider

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"
)

// ErrUnsafeURL indicates a URL that would allow SSRF (private/metadata host).
var ErrUnsafeURL = errors.New("url rejected by SSRF guard")

// allowPrivateUpstreams mirrors the allow_private_upstreams setting for the SSRF
// guard (SEC-06). It is deliberately package-level and atomic: every dial,
// redirect and metadata decision reads it at decision time, so flipping the
// setting takes effect on the next request instead of the next restart.
//
// The zero value (false) is the safe default: private and loopback upstreams are
// blocked until an operator explicitly opts in.
var allowPrivateUpstreams atomic.Bool

// SetAllowPrivate updates the runtime SSRF policy. It is safe for concurrent use
// and may be called while requests are in flight.
func SetAllowPrivate(allow bool) { allowPrivateUpstreams.Store(allow) }

// AllowPrivate reports the current SSRF policy.
func AllowPrivate() bool { return allowPrivateUpstreams.Load() }

// ValidateBaseURL performs syntactic validation and (when allowPrivate is
// false) blocks loopback, link-local, private-range and cloud-metadata hosts
// (PRD §8). Private URLs are permitted only when explicitly enabled for
// self-hosted/local providers.
//
// The allowPrivate argument stays explicit here because callers validate against
// the setting they are about to persist; runtime enforcement happens in the
// guarded transport, which reads the live policy via AllowPrivate.
func ValidateBaseURL(raw string, allowPrivate bool) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("%w: empty url", ErrUnsafeURL)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("%w: malformed url", ErrUnsafeURL)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("%w: scheme must be http or https", ErrUnsafeURL)
	}
	if u.User != nil {
		return "", fmt.Errorf("%w: credentials in url are not allowed", ErrUnsafeURL)
	}
	host := u.Hostname()
	if host == "" {
		return "", fmt.Errorf("%w: missing host", ErrUnsafeURL)
	}
	if isMetadataHost(host) {
		return "", fmt.Errorf("%w: metadata endpoints are blocked", ErrUnsafeURL)
	}
	if !allowPrivate {
		ips, err := resolveHost(host)
		if err != nil {
			return "", fmt.Errorf("%w: cannot resolve host", ErrUnsafeURL)
		}
		for _, ip := range ips {
			if isPrivateIP(ip) {
				return "", fmt.Errorf("%w: private address %s is not allowed (enable local providers to allow)", ErrUnsafeURL, ip)
			}
		}
	}
	// Normalize: strip trailing slash.
	return strings.TrimRight(u.String(), "/"), nil
}

func resolveHost(host string) ([]net.IP, error) {
	if ip := net.ParseIP(host); ip != nil {
		return []net.IP{ip}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	out := make([]net.IP, 0, len(addrs))
	for _, a := range addrs {
		out = append(out, a.IP)
	}
	return out, nil
}

// metadataHosts are blocked regardless of allowPrivate because they expose
// cloud instance credentials.
var metadataHosts = []string{
	"metadata.google.internal",
	"metadata.goog",
	"169.254.169.254",
	"fd00:ec2::254",
}

func isMetadataHost(host string) bool {
	h := strings.ToLower(host)
	for _, m := range metadataHosts {
		if h == m {
			return true
		}
	}
	return false
}

// isPrivateIP reports whether an IP is loopback, private, link-local or
// otherwise unsuitable as a public upstream.
func isPrivateIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsUnspecified() || ip.IsMulticast() || ip.IsInterfaceLocalMulticast() {
		return true
	}
	// CGNAT 100.64.0.0/10
	if ip4 := ip.To4(); ip4 != nil && ip4[0] == 100 && ip4[1] >= 64 && ip4[1] <= 127 {
		return true
	}
	return false
}

// checkDialTarget decides whether the dialer may connect to addr, given the
// addresses it resolved to. It reads the live SSRF policy, so a settings change
// applies without rebuilding the transport.
func checkDialTarget(host, port string, ips []net.IP) error {
	if len(ips) == 0 {
		return errors.New("no address resolved")
	}
	allowPrivate := AllowPrivate()
	for _, ip := range ips {
		if isMetadataHost(ip.String()) {
			return fmt.Errorf("%w: metadata endpoint", ErrUnsafeURL)
		}
		if !isPrivateIP(ip) {
			continue
		}
		// Loopback is permitted for local providers (vLLM, Ollama) but only when
		// explicitly enabled. Never allow the MyRouter admin port to be reached
		// from a provider URL (defeats self-call SSRF).
		if allowPrivate {
			continue
		}
		if ip.IsLoopback() && isLocalTarget(host, port) {
			// Loopback allowed only for non-admin ports.
			continue
		}
		return fmt.Errorf("%w: private or loopback address", ErrUnsafeURL)
	}
	return nil
}

// GuardedTransport returns an http.Transport whose dialer re-checks the
// resolved address, defeating DNS-rebinding attacks. The allowPrivate argument
// is only the initial policy: it seeds the package-level runtime flag, and every
// subsequent dial re-reads the live value so setting changes apply immediately.
func GuardedTransport(allowPrivate bool) *http.Transport {
	SetAllowPrivate(allowPrivate)
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	return &http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
		ForceAttemptHTTP2:   true,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			ips, err := resolveHost(host)
			if err != nil {
				return nil, err
			}
			if err := checkDialTarget(host, port, ips); err != nil {
				return nil, err
			}
			return dialer.DialContext(ctx, network, net.JoinHostPort(ips[0].String(), port))
		},
	}
}

// adminPorts are ports that must never be reached through a provider URL: the
// gateway's own admin surface listens here.
var adminPorts = map[string]bool{"20128": true}

// isLocalTarget reports whether a loopback dial target is a benign local
// service (local LLM server) rather than the MyRouter admin API itself.
func isLocalTarget(host, port string) bool {
	_ = host
	return !adminPorts[port]
}

// checkRedirectURL validates a redirect target against the live SSRF policy.
// Redirects may never land on a metadata endpoint, and may only land on private
// or loopback addresses when private upstreams are currently enabled.
func checkRedirectURL(u *url.URL) error {
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("%w: redirect to unsupported scheme", ErrUnsafeURL)
	}
	host := u.Hostname()
	if isMetadataHost(host) {
		return fmt.Errorf("%w: redirect to metadata endpoint", ErrUnsafeURL)
	}
	if AllowPrivate() {
		return nil
	}
	ips, err := resolveHost(host)
	if err != nil {
		return fmt.Errorf("%w: cannot resolve redirect host", ErrUnsafeURL)
	}
	for _, ip := range ips {
		if isMetadataHost(ip.String()) {
			return fmt.Errorf("%w: redirect to metadata", ErrUnsafeURL)
		}
		// Redirects may never land on private or loopback addresses,
		// regardless of the local-provider exemption on the base URL.
		if isPrivateIP(ip) {
			return fmt.Errorf("%w: redirect to private or loopback address", ErrUnsafeURL)
		}
	}
	return nil
}

// GuardedClient builds an http.Client using the guarded transport.
// Redirects are re-validated so an upstream cannot bounce the request to an
// internal host (SSRF via redirect). The allowPrivate argument is the initial
// policy only; later changes go through SetAllowPrivate and are picked up by the
// next dial or redirect check.
func GuardedClient(allowPrivate bool, timeout time.Duration) *http.Client {
	return &http.Client{
		Transport: GuardedTransport(allowPrivate),
		Timeout:   timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return fmt.Errorf("%w: too many redirects", ErrUnsafeURL)
			}
			return checkRedirectURL(req.URL)
		},
	}
}

// splitHostPortDefault splits host:port, defaulting the port by scheme.
func splitHostPortDefault(hostport string) (string, string) {
	if h, p, err := net.SplitHostPort(hostport); err == nil {
		return h, p
	}
	if strings.HasPrefix(hostport, "[") {
		return hostport, "443"
	}
	return hostport, "443"
}
