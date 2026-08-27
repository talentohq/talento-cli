package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/talentohq/talento-cli/internal/config"
)

const maxMetadataBytes = 1 << 20

type ProtectedResourceMetadata struct {
	Resource               string   `json:"resource"`
	AuthorizationServers   []string `json:"authorization_servers"`
	BearerMethodsSupported []string `json:"bearer_methods_supported"`
	ScopesSupported        []string `json:"scopes_supported"`
}

type AuthorizationServerMetadata struct {
	Issuer                            string   `json:"issuer"`
	AuthorizationEndpoint             string   `json:"authorization_endpoint"`
	TokenEndpoint                     string   `json:"token_endpoint"`
	RevocationEndpoint                string   `json:"revocation_endpoint,omitempty"`
	RegistrationEndpoint              string   `json:"registration_endpoint"`
	ScopesSupported                   []string `json:"scopes_supported"`
	ResponseTypesSupported            []string `json:"response_types_supported"`
	GrantTypesSupported               []string `json:"grant_types_supported"`
	TokenEndpointAuthMethodsSupported []string `json:"token_endpoint_auth_methods_supported"`
	CodeChallengeMethodsSupported     []string `json:"code_challenge_methods_supported"`
}

type Discovery struct {
	Resource      ProtectedResourceMetadata   `json:"resource_metadata"`
	Authorization AuthorizationServerMetadata `json:"authorization_server_metadata"`
}

func Discover(ctx context.Context, client *http.Client) (Discovery, error) {
	return DiscoverFrom(ctx, client, "https://mcp.talentohq.com/.well-known/oauth-protected-resource/mcp", config.Endpoint)
}

func DiscoverFrom(ctx context.Context, client *http.Client, resourceURL, expectedResource string) (Discovery, error) {
	if client == nil {
		client = http.DefaultClient
	}
	var resource ProtectedResourceMetadata
	if err := getJSON(ctx, client, resourceURL, &resource); err != nil {
		return Discovery{}, fmt.Errorf("discover protected resource: %w", err)
	}
	if normalizeURL(resource.Resource) != normalizeURL(expectedResource) {
		return Discovery{}, fmt.Errorf("protected resource mismatch: got %q, want %q", resource.Resource, expectedResource)
	}
	if len(resource.AuthorizationServers) != 1 {
		return Discovery{}, fmt.Errorf("expected exactly one authorization server, got %d", len(resource.AuthorizationServers))
	}
	issuer := normalizeURL(resource.AuthorizationServers[0])
	if err := validateHTTPSURL(issuer); err != nil {
		return Discovery{}, fmt.Errorf("invalid authorization server: %w", err)
	}
	if !contains(resource.ScopesSupported, config.Scope) {
		return Discovery{}, fmt.Errorf("protected resource does not advertise required scope %q", config.Scope)
	}

	metadataURL := issuer + "/.well-known/oauth-authorization-server"
	var server AuthorizationServerMetadata
	if err := getJSON(ctx, client, metadataURL, &server); err != nil {
		return Discovery{}, fmt.Errorf("discover authorization server: %w", err)
	}
	if normalizeURL(server.Issuer) != issuer {
		return Discovery{}, fmt.Errorf("authorization issuer mismatch: got %q, want %q", server.Issuer, issuer)
	}
	if !contains(server.ScopesSupported, config.Scope) {
		return Discovery{}, fmt.Errorf("authorization server does not advertise required scope %q", config.Scope)
	}
	if !contains(server.GrantTypesSupported, "authorization_code") || !contains(server.GrantTypesSupported, "refresh_token") {
		return Discovery{}, fmt.Errorf("authorization server must support authorization_code and refresh_token")
	}
	if !contains(server.CodeChallengeMethodsSupported, "S256") {
		return Discovery{}, fmt.Errorf("authorization server does not support S256 PKCE")
	}
	if !contains(server.TokenEndpointAuthMethodsSupported, "none") {
		return Discovery{}, fmt.Errorf("authorization server does not support public clients")
	}
	for label, endpoint := range map[string]string{
		"authorization": server.AuthorizationEndpoint,
		"token":         server.TokenEndpoint,
		"registration":  server.RegistrationEndpoint,
	} {
		if err := validateIssuerEndpoint(issuer, endpoint); err != nil {
			return Discovery{}, fmt.Errorf("invalid %s endpoint: %w", label, err)
		}
	}
	if server.RevocationEndpoint != "" {
		if err := validateIssuerEndpoint(issuer, server.RevocationEndpoint); err != nil {
			return Discovery{}, fmt.Errorf("invalid revocation endpoint: %w", err)
		}
	}
	return Discovery{Resource: resource, Authorization: server}, nil
}

func getJSON(ctx context.Context, client *http.Client, endpoint string, target any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	dec := json.NewDecoder(io.LimitReader(resp.Body, maxMetadataBytes))
	if err := dec.Decode(target); err != nil {
		return fmt.Errorf("decode JSON: %w", err)
	}
	return nil
}

func validateHTTPSURL(value string) error {
	u, err := url.Parse(value)
	if err != nil {
		return err
	}
	if u.Scheme != "https" || u.Host == "" || u.User != nil {
		return fmt.Errorf("expected an absolute HTTPS URL")
	}
	return nil
}

func validateIssuerEndpoint(issuer, endpoint string) error {
	if err := validateHTTPSURL(endpoint); err != nil {
		return err
	}
	i, _ := url.Parse(issuer)
	e, _ := url.Parse(endpoint)
	if !strings.EqualFold(i.Host, e.Host) {
		return fmt.Errorf("endpoint host %q does not match issuer host %q", e.Host, i.Host)
	}
	return nil
}

func normalizeURL(value string) string {
	return strings.TrimRight(strings.TrimSpace(value), "/")
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
