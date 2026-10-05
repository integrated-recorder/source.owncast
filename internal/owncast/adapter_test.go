package owncast

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/integrated-recorder/adapter-sdk-go/adapter"
	"github.com/integrated-recorder/adapter-sdk-go/protocol"
)

func TestResolveOwncastURL(t *testing.T) {
	for _, test := range []struct{ input, want string }{
		{"https://live.example/custom/base/?token=ignored#section", "https://live.example/custom/base" + StreamPath},
		{"https://live.example/custom/base?", "https://live.example/custom/base" + StreamPath},
		{"http://live.example", "http://live.example" + StreamPath},
		{"https://live.example/live?channel=1", "https://live.example/live" + StreamPath},
		{"https://live.example/extensionless/manifest?x=1", "https://live.example/extensionless/manifest" + StreamPath},
	} {
		got, err := resolveURL(test.input)
		if err != nil || got != test.want {
			t.Errorf("resolveURL(%q) = %q, %v; want %q", test.input, got, err, test.want)
		}
	}
}

func TestResolveRejectsInvalidURLsAndInputShapes(t *testing.T) {
	for _, input := range []string{
		``, `null`, `[]`, `{"source_url":""}`, `{"source_url":"file:///tmp/live"}`,
		`{"source_url":"https:///missing-host"}`, `{"source_url":"https://user:pass@live.example"}`,
		`{"source_url":"//live.example"}`, `{"source_url":"https://live.example","other":true}`,
		`{"source_url":"https://live.example","source_url":"https://other.example"}`,
		`{"source_url":"https://local\u0000host.example"}`, `{"source_url":"https://live.example:99999"}`,
	} {
		if _, err := resolveInput(json.RawMessage(input)); err == nil {
			t.Errorf("resolveInput(%q) succeeded", input)
		}
	}
	if _, err := resolveInput(bytes.Repeat([]byte("x"), maxInputBytes+1)); err == nil {
		t.Fatal("oversized input accepted")
	}
}

func TestDescriptorIsProtocolV1Owncast(t *testing.T) {
	plugin := New()
	d := plugin.Descriptor()
	if err := adapter.Validate(plugin); err != nil {
		t.Fatal(err)
	}
	if d.ID != "owncast" || d.Name != "Owncast" || d.Version != "0.2.0-dev" || d.ProtocolVersion != protocol.Version {
		t.Fatalf("descriptor identity = %#v", d)
	}
	if !slices.Equal(d.Capabilities, []string{protocol.CapabilityResolve, protocol.CapabilityWatch, protocol.CapabilityMetadata}) || !slices.Equal(d.MediaTypes, []string{"hls"}) {
		t.Fatalf("descriptor capabilities/media = %#v / %#v", d.Capabilities, d.MediaTypes)
	}
	if d.Branding != nil {
		t.Fatalf("descriptor unexpectedly embeds separately licensed branding: %#v", d.Branding)
	}
}

func TestResolveReturnsHLSMedia(t *testing.T) {
	got, err := New().Resolve(context.Background(), protocol.ResolveParams{Input: json.RawMessage(`{"source_url":"https://live.example/base?token=ignored#fragment"}`)})
	if err != nil || got.Media.Type != "hls" || got.Media.ManifestURL != "https://live.example/base"+StreamPath {
		t.Fatalf("resolve = %#v, %v", got, err)
	}
	_, err = New().Resolve(context.Background(), protocol.ResolveParams{Input: json.RawMessage(`{"source_url":"file:///etc/passwd"}`)})
	var operationErr *adapter.OperationError
	if !errors.As(err, &operationErr) || operationErr.Code != "invalid_input" {
		t.Fatalf("invalid resolve error = %#v", err)
	}
}

func TestWatchLiveStatusIncludesMediaTitleAndSessionStart(t *testing.T) {
	var seenPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenPath = r.URL.Path
		if r.Method != http.MethodGet {
			t.Errorf("method = %s", r.Method)
		}
		_, _ = io.WriteString(w, `{"online":true,"lastConnectTime":"2026-09-28T01:02:03Z","streamTitle":"Broadcast title"}`)
	}))
	defer server.Close()
	validated := ""
	deadlineSeen := false
	plugin := newWithDependencies(server.Client(), func(ctx context.Context, raw string) error {
		validated = raw
		_, deadlineSeen = ctx.Deadline()
		return nil
	})
	result, err := plugin.WatchCheck(context.Background(), protocol.WatchCheckParams{Input: json.RawMessage(`{"source_url":"` + server.URL + `/custom/base?token=ignored#fragment"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if seenPath != "/custom/base"+statusPath || !strings.HasSuffix(validated, "/custom/base"+statusPath) || !deadlineSeen {
		t.Fatalf("status request path=%q validated=%q deadline=%v", seenPath, validated, deadlineSeen)
	}
	if result.State != "live" || result.Media == nil || result.Media.ManifestURL != server.URL+"/custom/base"+StreamPath || result.SessionRef != "2026-09-28T01:02:03Z" || result.StartedAt == nil {
		t.Fatalf("live result = %#v", result)
	}
	if result.StartedAt.Location() != time.UTC || result.Title != "Broadcast title" {
		t.Fatalf("live metadata = %#v", result)
	}
}

func TestWatchOfflineOnlyForSuccessfulOfflineObservation(t *testing.T) {
	for _, test := range []struct {
		name        string
		code        int
		body        string
		wantOffline bool
	}{
		{name: "offline", code: http.StatusOK, body: `{"online":false}`, wantOffline: true},
		{name: "missing online", code: http.StatusOK, body: `{"streaming":false}`},
		{name: "malformed json", code: http.StatusOK, body: `{`},
		{name: "invalid session timestamp", code: http.StatusOK, body: `{"online":true,"lastConnectTime":"not-a-time"}`},
		{name: "server error", code: http.StatusServiceUnavailable, body: `{"online":false}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(test.code)
				_, _ = io.WriteString(w, test.body)
			}))
			defer server.Close()
			plugin := newWithDependencies(server.Client(), func(context.Context, string) error { return nil })
			result, err := plugin.WatchCheck(context.Background(), protocol.WatchCheckParams{Input: json.RawMessage(`{"source_url":"` + server.URL + `"}`)})
			if test.wantOffline {
				if err != nil || result.State != "offline" || result.Media != nil {
					t.Fatalf("offline result=%#v error=%v", result, err)
				}
				return
			}
			if err == nil || result.State == "offline" {
				t.Fatalf("invalid status treated as offline: result=%#v error=%v", result, err)
			}
		})
	}
}

func TestWatchMalformedOptionalTitleDoesNotBlockLiveButMetadataRejects(t *testing.T) {
	title := strings.Repeat("x", 4<<10+1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"online": true, "streamTitle": title})
	}))
	defer server.Close()
	plugin := newWithDependencies(server.Client(), func(context.Context, string) error { return nil })
	input := json.RawMessage(`{"source_url":"` + server.URL + `"}`)
	result, err := plugin.WatchCheck(context.Background(), protocol.WatchCheckParams{Input: input})
	if err != nil || result.State != "live" || result.Media == nil || result.Title != "" {
		t.Fatalf("invalid optional title blocked live observation: result=%#v err=%v", result, err)
	}
	if _, err := plugin.Metadata(context.Background(), protocol.MetadataParams{Current: *result.Media}); err == nil {
		t.Fatal("oversized source metadata title was accepted")
	}
}

func TestMetadataPreservesKnownEmptyTitle(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != statusPath || r.URL.RawQuery != "" {
			t.Errorf("path/query = %q / %q", r.URL.Path, r.URL.RawQuery)
		}
		_, _ = io.WriteString(w, `{"online":true,"streamTitle":""}`)
	}))
	defer server.Close()
	plugin := newWithDependencies(server.Client(), func(context.Context, string) error { return nil })
	result, err := plugin.Metadata(context.Background(), protocol.MetadataParams{Current: protocol.MediaSource{Type: "hls", ManifestURL: server.URL + StreamPath + "?signed=ignored#fragment"}})
	if err != nil || result.Metadata.Title == nil || *result.Metadata.Title != "" || result.Metadata.Description != nil || result.SourceUpdatedAt != nil {
		t.Fatalf("metadata = %#v, %v", result, err)
	}
}

func TestWatchAuthenticationFailureIsSafeProtocolError(t *testing.T) {
	for _, statusCode := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		t.Run(http.StatusText(statusCode), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(statusCode)
				_, _ = io.WriteString(w, "response-body-secret-sentinel")
			}))
			defer server.Close()
			plugin := newWithDependencies(server.Client(), func(context.Context, string) error { return nil })
			rawRequest := `{"protocol_version":1,"id":"request-1","method":"watch.check","params":{"input":{"source_url":"` + server.URL + `?sensitive=query"}}}`
			var output bytes.Buffer
			if err := adapter.ServeIO(context.Background(), plugin, strings.NewReader(rawRequest+"\n"), &output); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(output.String(), "secret-sentinel") || strings.Contains(output.String(), server.URL) || strings.Contains(output.String(), "sensitive") {
				t.Fatalf("protocol response leaked private data: %s", output.String())
			}
			if !strings.Contains(output.String(), `"code":"authentication_required"`) || !strings.Contains(output.String(), `"message":"Owncast status endpoint requires authentication"`) {
				t.Fatalf("authentication error was not safe/structured: %s", output.String())
			}
		})
	}
}

func TestMetadataAuthenticationFailureUsesSafeCode(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, "do-not-return-this-body")
	}))
	defer server.Close()
	plugin := newWithDependencies(server.Client(), func(context.Context, string) error { return nil })
	_, err := plugin.Metadata(context.Background(), protocol.MetadataParams{Current: protocol.MediaSource{Type: "hls", ManifestURL: server.URL + StreamPath}})
	var operationErr *adapter.OperationError
	if !errors.As(err, &operationErr) || operationErr.Code != "authentication_required" || strings.Contains(err.Error(), server.URL) || strings.Contains(err.Error(), "do-not-return") {
		t.Fatalf("metadata authentication error = %#v", err)
	}
}

func TestStatusBodyLimitAndValidationContext(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, strings.Repeat("x", maxStatusBytes+1))
	}))
	defer server.Close()
	plugin := newWithDependencies(server.Client(), func(ctx context.Context, _ string) error {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 9*time.Second {
			t.Fatalf("validation context deadline missing or unbounded: %v %v", deadline, ok)
		}
		return nil
	})
	result, err := plugin.WatchCheck(context.Background(), protocol.WatchCheckParams{Input: json.RawMessage(`{"source_url":"` + server.URL + `"}`)})
	if err == nil || result.State == "offline" {
		t.Fatalf("oversized status result=%#v error=%v", result, err)
	}
}
