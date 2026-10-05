package owncast

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/integrated-recorder/adapter-sdk-go/adapter"
	"github.com/integrated-recorder/adapter-sdk-go/protocol"
)

const (
	StreamPath     = "/hls/stream.m3u8"
	statusPath     = "/api/status"
	maxStatusBytes = 64 << 10
	maxInputBytes  = 4 << 10
	maxURLBytes    = 2 << 10
	requestTimeout = 8 * time.Second
)

// version is set from the release tag by the release workflow. Local builds
// identify themselves as development builds rather than impersonating a
// released plugin version.
var version = "0.2.0-dev"

type urlValidator func(context.Context, string) error

type Plugin struct {
	client   *http.Client
	validate urlValidator
}

var (
	_ adapter.Adapter          = (*Plugin)(nil)
	_ adapter.Resolver         = (*Plugin)(nil)
	_ adapter.Watcher          = (*Plugin)(nil)
	_ adapter.MetadataProvider = (*Plugin)(nil)
)

// New constructs the production plugin with the public-network-only client.
func New() *Plugin {
	return &Plugin{client: newPublicHTTPClient(netDefaultResolver{}, netDefaultDialer{}, requestTimeout), validate: validatePublicURL}
}

func newWithDependencies(client *http.Client, validate urlValidator) *Plugin {
	return &Plugin{client: client, validate: validate}
}

func (p *Plugin) Descriptor() protocol.Descriptor {
	return protocol.Descriptor{
		ID: "owncast", Name: "Owncast", Version: version, ProtocolVersion: protocol.Version,
		Capabilities: []string{protocol.CapabilityResolve, protocol.CapabilityWatch, protocol.CapabilityMetadata},
		InputSchema: protocol.Schema{Fields: []protocol.Field{{
			Key: "source_url", Control: "text", Label: "Owncast instance URL",
			Description: "Base URL of the Owncast instance.", Required: true,
		}}},
		ConfigurationSchema: protocol.Schema{Fields: []protocol.Field{}},
		ResourceTypes:       []protocol.ResourceType{},
		MediaTypes:          []string{"hls"},
	}
}

func (p *Plugin) Resolve(ctx context.Context, params protocol.ResolveParams) (protocol.ResolveResult, error) {
	manifest, err := resolveInput(params.Input)
	if err != nil {
		return protocol.ResolveResult{}, adapter.Error("invalid_input", "input must include a valid Owncast instance URL")
	}
	return protocol.ResolveResult{Media: protocol.MediaSource{Type: "hls", ManifestURL: manifest}}, nil
}

func (p *Plugin) WatchCheck(ctx context.Context, params protocol.WatchCheckParams) (protocol.WatchCheckResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	manifest, err := resolveInput(params.Input)
	if err != nil {
		return protocol.WatchCheckResult{}, adapter.Error("invalid_input", "input must include a valid Owncast instance URL")
	}
	if p == nil || p.client == nil || p.validate == nil {
		return protocol.WatchCheckResult{}, fmt.Errorf("Owncast status dependencies are unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	status, err := fetchStatus(ctx, manifest, p.client, p.validate)
	if err != nil {
		if safeErrorCode(err) == "authentication_required" {
			return protocol.WatchCheckResult{}, adapter.Error("authentication_required", "Owncast status endpoint requires authentication")
		}
		return protocol.WatchCheckResult{}, err
	}
	if !*status.Online {
		return protocol.WatchCheckResult{State: "offline"}, nil
	}
	result := protocol.WatchCheckResult{State: "live", Media: &protocol.MediaSource{Type: "hls", ManifestURL: manifest}}
	// Keep a malformed optional status title from blocking an otherwise valid
	// live observation. Metadata polling validates title strictly.
	if status.StreamTitle != nil && validMetadataText(status.StreamTitle) {
		result.Title = *status.StreamTitle
	}
	if len(status.LastConnectTime) > 0 && string(status.LastConnectTime) != "null" {
		var value string
		if json.Unmarshal(status.LastConnectTime, &value) != nil || len(value) > 128 {
			return protocol.WatchCheckResult{}, fmt.Errorf("status response is invalid")
		}
		startedAt, parseErr := time.Parse(time.RFC3339, value)
		if parseErr != nil {
			return protocol.WatchCheckResult{}, fmt.Errorf("status response is invalid")
		}
		startedAt = startedAt.UTC()
		result.SessionRef = value
		result.StartedAt = &startedAt
	}
	if err := result.Validate(); err != nil {
		return protocol.WatchCheckResult{}, fmt.Errorf("status response is invalid")
	}
	return result, nil
}

func (p *Plugin) Metadata(ctx context.Context, params protocol.MetadataParams) (protocol.MetadataResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if params.Current.Type != "hls" {
		return protocol.MetadataResult{}, fmt.Errorf("unsupported media type")
	}
	if p == nil || p.client == nil || p.validate == nil {
		return protocol.MetadataResult{}, fmt.Errorf("Owncast status dependencies are unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	status, err := fetchStatus(ctx, params.Current.ManifestURL, p.client, p.validate)
	if err != nil {
		if safeErrorCode(err) == "authentication_required" {
			return protocol.MetadataResult{}, adapter.Error("authentication_required", "Owncast status endpoint requires authentication")
		}
		return protocol.MetadataResult{}, err
	}
	result := protocol.MetadataResult{}
	if status.StreamTitle != nil {
		result.Metadata.Title = status.StreamTitle
	}
	if err := result.Validate(); err != nil {
		return protocol.MetadataResult{}, fmt.Errorf("status metadata is invalid")
	}
	return result, nil
}

type owncastStatus struct {
	Online          *bool           `json:"online"`
	LastConnectTime json.RawMessage `json:"lastConnectTime,omitempty"`
	StreamTitle     *string         `json:"streamTitle"`
}

type safeStatusError struct{ code string }

func (e *safeStatusError) Error() string { return "Owncast status request failed" }

func safeErrorCode(err error) string {
	if e, ok := err.(*safeStatusError); ok {
		return e.code
	}
	return ""
}

func fetchStatus(ctx context.Context, manifestURL string, client *http.Client, validate urlValidator) (owncastStatus, error) {
	manifest, err := url.Parse(manifestURL)
	if err != nil || validateOwncastURL(manifestURL) != nil {
		return owncastStatus{}, fmt.Errorf("invalid source URL")
	}
	statusURL := *manifest
	statusURL.Path = strings.TrimSuffix(strings.TrimSuffix(statusURL.Path, StreamPath), "/") + statusPath
	statusURL.RawPath = ""
	statusURL.RawQuery = ""
	statusURL.ForceQuery = false
	statusURL.Fragment = ""
	if err := validate(ctx, statusURL.String()); err != nil {
		return owncastStatus{}, fmt.Errorf("status URL is not allowed")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, statusURL.String(), nil)
	if err != nil {
		return owncastStatus{}, fmt.Errorf("status request could not be created")
	}
	response, err := client.Do(request)
	if err != nil {
		return owncastStatus{}, fmt.Errorf("status request failed")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
			return owncastStatus{}, &safeStatusError{code: "authentication_required"}
		}
		return owncastStatus{}, fmt.Errorf("status service returned an error")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxStatusBytes+1))
	if err != nil || len(data) > maxStatusBytes || !utf8.Valid(data) {
		return owncastStatus{}, fmt.Errorf("status response is invalid")
	}
	var status owncastStatus
	if err := json.Unmarshal(data, &status); err != nil || status.Online == nil {
		return owncastStatus{}, fmt.Errorf("status response is invalid")
	}
	return status, nil
}

func resolveInput(input json.RawMessage) (string, error) {
	if len(input) == 0 || len(input) > maxInputBytes || !utf8.Valid(input) {
		return "", fmt.Errorf("invalid input")
	}
	decoder := json.NewDecoder(strings.NewReader(string(input)))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return "", fmt.Errorf("input must be an object")
	}
	seen := false
	var sourceURL string
	for decoder.More() {
		token, err = decoder.Token()
		key, ok := token.(string)
		if err != nil || !ok || key != "source_url" || seen {
			return "", fmt.Errorf("invalid input field")
		}
		seen = true
		if err := decoder.Decode(&sourceURL); err != nil {
			return "", fmt.Errorf("invalid source URL")
		}
	}
	if _, err := decoder.Token(); err != nil {
		return "", fmt.Errorf("invalid input object")
	}
	if _, err := decoder.Token(); err != io.EOF {
		return "", fmt.Errorf("trailing input")
	}
	if !seen {
		return "", fmt.Errorf("source_url is required")
	}
	return resolveURL(sourceURL)
}

func resolveURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if len(raw) == 0 || len(raw) > maxURLBytes || !utf8.ValidString(raw) || strings.ContainsAny(raw, "\\\r\n\t") {
		return "", fmt.Errorf("source URL is invalid")
	}
	u, err := url.Parse(raw)
	if err != nil || validateOwncastURL(raw) != nil {
		return "", fmt.Errorf("source URL must be an http or https instance URL")
	}
	u.Path = strings.TrimRight(u.Path, "/") + StreamPath
	u.RawPath = ""
	u.RawQuery = ""
	u.ForceQuery = false
	u.Fragment = ""
	resolved := u.String()
	if len(resolved) > maxURLBytes {
		return "", fmt.Errorf("source URL is too long")
	}
	return resolved, nil
}

func validMetadataText(value *string) bool {
	return value != nil && len(*value) <= 4<<10 && utf8.ValidString(*value) && !strings.ContainsRune(*value, 0)
}

// validateOwncastURL validates URL syntax only. Core remains authoritative
// for public-network enforcement on the resolved media URL.
func validateOwncastURL(raw string) error {
	if len(raw) == 0 || len(raw) > maxURLBytes || !utf8.ValidString(raw) || strings.ContainsAny(raw, "\\\r\n\t") {
		return fmt.Errorf("invalid URL")
	}
	u, err := url.Parse(raw)
	if err != nil || u == nil || u.Opaque != "" || u.User != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return fmt.Errorf("invalid URL")
	}
	host := u.Hostname()
	if strings.Contains(host, "%") || strings.HasSuffix(u.Host, ":") {
		return fmt.Errorf("invalid URL host")
	}
	if !validPort(u.Port()) {
		return fmt.Errorf("invalid URL port")
	}
	return nil
}
