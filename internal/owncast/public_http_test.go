package owncast

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type staticResolver map[string][]net.IPAddr

func (r staticResolver) LookupIPAddr(_ context.Context, host string) ([]net.IPAddr, error) {
	return r[host], nil
}

type mappingDialer struct {
	connectTo string
	seen      atomic.Value
}

func (d *mappingDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	d.seen.Store(address)
	return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, network, d.connectTo)
}

func TestPublicAddressPolicy(t *testing.T) {
	for _, raw := range []string{
		"0.0.0.0", "10.0.0.1", "100.64.0.1", "127.0.0.1", "169.254.1.2", "172.16.0.1", "192.168.1.1",
		"198.18.0.1", "203.0.113.1", "224.0.0.1", "::", "::1", "fc00::1", "fe80::1", "2001:db8::1", "2002::1", "3fff::1", "5f00::1",
	} {
		address := netip.MustParseAddr(raw)
		if isPublicAddress(address) {
			t.Errorf("%s accepted as public", raw)
		}
	}
	for _, raw := range []string{"8.8.8.8", "1.1.1.1", "2606:4700:4700::1111"} {
		if !isPublicAddress(netip.MustParseAddr(raw)) {
			t.Errorf("%s rejected as public", raw)
		}
	}
}

func TestPublicURLValidationRejectsSpecialHostsAndPrivateDNS(t *testing.T) {
	resolver := staticResolver{
		"public.example.com":  {net.IPAddr{IP: net.ParseIP("8.8.8.8")}},
		"mixed.example.net":   {net.IPAddr{IP: net.ParseIP("8.8.8.8")}, net.IPAddr{IP: net.ParseIP("127.0.0.1")}},
		"private.example.net": {net.IPAddr{IP: net.ParseIP("192.168.1.9")}},
	}
	for _, raw := range []string{"http://127.0.0.1/", "http://localhost/", "http://foo.local/", "http://127.1/", "http://user:pass@public.example.com/", "file:///etc/passwd", "http://[fe80::1%25en0]/"} {
		if err := validatePublicURLWithResolver(context.Background(), raw, resolver); err == nil {
			t.Errorf("unsafe URL accepted: %q", raw)
		}
	}
	for _, raw := range []string{"http://private.example.net/", "http://mixed.example.net/"} {
		if err := validatePublicURLWithResolver(context.Background(), raw, resolver); err == nil {
			t.Errorf("non-public DNS answer accepted: %q", raw)
		}
	}
	if err := validatePublicURLWithResolver(context.Background(), "https://public.example.com/path", resolver); err != nil {
		t.Fatalf("public hostname rejected: %v", err)
	}
}

func TestPublicHTTPClientPinsValidatedAddressAndDoesNotUseProxy(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "public.example.com" {
			t.Errorf("Host = %q", r.Host)
		}
		_, _ = w.Write([]byte("ok"))
	}))
	defer server.Close()
	dialer := &mappingDialer{connectTo: server.Listener.Addr().String()}
	resolver := staticResolver{"public.example.com": {net.IPAddr{IP: net.ParseIP("8.8.8.8")}}}
	client := newPublicHTTPClient(resolver, dialer, time.Second)
	response, err := client.Get("http://public.example.com/path")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if got, _ := dialer.seen.Load().(string); got != "8.8.8.8:80" {
		t.Fatalf("dial target = %q, want pinned public IP", got)
	}
}

func TestPublicHTTPClientRevalidatesRedirectTarget(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", "http://private.example.net/secret")
		w.WriteHeader(http.StatusFound)
	}))
	defer server.Close()
	publicURL := strings.Replace(server.URL, "127.0.0.1", "public.example.com", 1)
	resolver := staticResolver{
		"public.example.com":  {net.IPAddr{IP: net.ParseIP("8.8.8.8")}},
		"private.example.net": {net.IPAddr{IP: net.ParseIP("10.2.3.4")}},
	}
	dialer := &mappingDialer{connectTo: server.Listener.Addr().String()}
	client := newPublicHTTPClient(resolver, dialer, time.Second)
	_, err := client.Get(publicURL)
	if err == nil || !strings.Contains(err.Error(), "redirect") {
		t.Fatalf("redirect to private address was not rejected: %v", err)
	}
}

func TestPublicHTTPClientRevalidatesDNSAtDialToResistRebinding(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("private rebinding target was contacted")
	}))
	defer server.Close()
	var lookups atomic.Int32
	resolver := resolverFunc(func(context.Context, string) ([]net.IPAddr, error) {
		if lookups.Add(1) == 1 {
			return []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}}, nil
		}
		return []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}, nil
	})
	if err := validatePublicURLWithResolver(context.Background(), "http://rebinding.example.com/", resolver); err != nil {
		t.Fatalf("initial public DNS answer rejected: %v", err)
	}
	client := newPublicHTTPClient(resolver, &mappingDialer{connectTo: server.Listener.Addr().String()}, time.Second)
	_, err := client.Get("http://rebinding.example.com/")
	if err == nil {
		t.Fatal("DNS-rebound private address was accepted")
	}
}

func TestPublicHTTPClientBoundsRedirectCount(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hop := r.URL.Query().Get("hop")
		if hop == "" {
			hop = "0"
		}
		w.Header().Set("Location", "/?hop="+hop+"x")
		w.WriteHeader(http.StatusFound)
	}))
	defer server.Close()
	resolver := staticResolver{"public.example.com": {net.IPAddr{IP: net.ParseIP("8.8.8.8")}}}
	dialer := &mappingDialer{connectTo: server.Listener.Addr().String()}
	client := newPublicHTTPClient(resolver, dialer, time.Second)
	_, err := client.Get(strings.Replace(server.URL, "127.0.0.1", "public.example.com", 1))
	if err == nil {
		t.Fatal("unbounded redirect loop was accepted")
	}
}

type resolverFunc func(context.Context, string) ([]net.IPAddr, error)

func (f resolverFunc) LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error) {
	return f(ctx, host)
}
