package owncast

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const maxRedirects = 5

type ipResolver interface {
	LookupIPAddr(context.Context, string) ([]net.IPAddr, error)
}

type contextDialer interface {
	DialContext(context.Context, string, string) (net.Conn, error)
}

type netDefaultResolver struct{}

func (netDefaultResolver) LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error) {
	return net.DefaultResolver.LookupIPAddr(ctx, host)
}

type netDefaultDialer struct{}

func (netDefaultDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return (&net.Dialer{Timeout: 4 * time.Second, KeepAlive: -1}).DialContext(ctx, network, address)
}

func newPublicHTTPClient(resolver ipResolver, dialer contextDialer, timeout time.Duration) *http.Client {
	if resolver == nil {
		resolver = netDefaultResolver{}
	}
	if dialer == nil {
		dialer = netDefaultDialer{}
	}
	if timeout <= 0 {
		timeout = requestTimeout
	}
	transport := &http.Transport{
		// Never inherit HTTP_PROXY/HTTPS_PROXY from the process environment.
		Proxy:                 nil,
		DisableKeepAlives:     true,
		ForceAttemptHTTP2:     false,
		TLSHandshakeTimeout:   4 * time.Second,
		ResponseHeaderTimeout: 5 * time.Second,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(address)
			if err != nil || !validPort(port) {
				return nil, fmt.Errorf("network target is invalid")
			}
			ips, err := publicAddresses(ctx, host, resolver)
			if err != nil {
				return nil, err
			}
			var lastErr error
			for _, ip := range ips {
				connection, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
				if dialErr == nil {
					return connection, nil
				}
				lastErr = dialErr
			}
			if lastErr == nil {
				lastErr = fmt.Errorf("no public address")
			}
			return nil, fmt.Errorf("network connection failed")
		},
		TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12},
	}
	client := &http.Client{Transport: transport, Timeout: timeout}
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) > maxRedirects {
			return fmt.Errorf("redirect limit exceeded")
		}
		if err := validatePublicURLWithResolver(req.Context(), req.URL.String(), resolver); err != nil {
			return fmt.Errorf("redirect target is not allowed")
		}
		return nil
	}
	return client
}

func validatePublicURL(ctx context.Context, raw string) error {
	return validatePublicURLWithResolver(ctx, raw, netDefaultResolver{})
}

func validatePublicURLWithResolver(ctx context.Context, raw string, resolver ipResolver) error {
	if err := validateOwncastURL(raw); err != nil {
		return fmt.Errorf("URL is not allowed")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("URL is not allowed")
	}
	if _, err := publicAddresses(ctx, u.Hostname(), resolver); err != nil {
		return fmt.Errorf("URL is not allowed")
	}
	return nil
}

func publicAddresses(ctx context.Context, host string, resolver ipResolver) ([]net.IP, error) {
	if resolver == nil || host == "" || strings.Contains(host, "%") {
		return nil, fmt.Errorf("URL host is not allowed")
	}
	if literal, err := netip.ParseAddr(host); err == nil {
		if literal.Zone() != "" || !isPublicAddress(literal) {
			return nil, fmt.Errorf("URL host is not public")
		}
		literal = literal.Unmap()
		return []net.IP{net.IP(literal.AsSlice())}, nil
	}
	if !validPublicHostname(host) {
		return nil, fmt.Errorf("URL host is not public")
	}
	resolved, err := resolver.LookupIPAddr(ctx, strings.TrimSuffix(host, "."))
	if err != nil || len(resolved) == 0 || len(resolved) > 32 {
		return nil, fmt.Errorf("URL host could not be resolved")
	}
	addresses := make([]net.IP, 0, len(resolved))
	for _, item := range resolved {
		if item.Zone != "" {
			return nil, fmt.Errorf("URL host is not public")
		}
		address, ok := netip.AddrFromSlice(item.IP)
		if !ok {
			return nil, fmt.Errorf("URL host is not public")
		}
		if address.Is4In6() {
			address = address.Unmap()
		}
		if !isPublicAddress(address) {
			return nil, fmt.Errorf("URL host is not public")
		}
		addresses = append(addresses, net.IP(address.AsSlice()))
	}
	return addresses, nil
}

func validPublicHostname(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if len(host) == 0 || len(host) > 253 || !strings.Contains(host, ".") || strings.Contains(host, "..") {
		return false
	}
	if strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal") || strings.HasSuffix(host, ".test") || strings.HasSuffix(host, ".example") || strings.HasSuffix(host, ".invalid") || strings.HasSuffix(host, ".home.arpa") || strings.HasSuffix(host, ".onion") {
		return false
	}
	numeric := true
	for _, r := range host {
		if (r < '0' || r > '9') && r != '.' {
			numeric = false
			break
		}
	}
	if numeric {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
				return false
			}
		}
	}
	return true
}

var deniedNetworks = func() []netip.Prefix {
	raw := []string{
		"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16",
		"172.16.0.0/12", "192.0.0.0/24", "192.0.2.0/24", "192.88.99.0/24", "192.168.0.0/16",
		"198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "224.0.0.0/4", "240.0.0.0/4",
		"64:ff9b::/96", "64:ff9b:1::/48", "100::/64", "100:0:0:1::/64", "2001::/23", "2001:db8::/32", "2002::/16",
		"fec0::/10", "3fff::/20", "5f00::/16",
	}
	prefixes := make([]netip.Prefix, 0, len(raw))
	for _, value := range raw {
		prefixes = append(prefixes, netip.MustParsePrefix(value))
	}
	return prefixes
}()

func isPublicAddress(address netip.Addr) bool {
	if address.Is4In6() {
		address = address.Unmap()
	}
	if !address.IsValid() || !address.IsGlobalUnicast() || address.IsPrivate() || address.IsLoopback() || address.IsLinkLocalUnicast() || address.IsMulticast() || address.IsUnspecified() {
		return false
	}
	for _, prefix := range deniedNetworks {
		if prefix.Contains(address) {
			return false
		}
	}
	return true
}

func validPort(raw string) bool {
	if raw == "" {
		return true
	}
	port, err := strconv.Atoi(raw)
	return err == nil && port > 0 && port <= 65535
}
