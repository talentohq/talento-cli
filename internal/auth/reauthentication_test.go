package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type reauthenticationTransport func(*http.Request) (*http.Response, error)

func (f reauthenticationTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func recoveryService(t *testing.T, credentials Credentials) (*Service, *memoryCredentials) {
	t.Helper()
	store := &memoryCredentials{values: map[string]Credentials{"acme": credentials}}
	service := NewService(nil, store)
	service.Now = func() time.Time { return time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC) }
	service.OpenBrowser = func(string) error {
		t.Fatal("token failure must not open a browser")
		return nil
	}
	service.Discover = func(context.Context, *http.Client) (Discovery, error) {
		return Discovery{Authorization: AuthorizationServerMetadata{TokenEndpoint: "https://issuer.example/token"}}, nil
	}
	return service, store
}

func TestInvalidStoredCredentialsRequireExplicitSignInWithoutChangingDiagnostics(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*Credentials)
	}{
		{"missing access token", func(c *Credentials) { c.AccessToken = "" }},
		{"invalid token type", func(c *Credentials) { c.TokenType = "MAC" }},
		{"missing scope", func(c *Credentials) { c.Scope = "" }},
		{"wrong resource", func(c *Credentials) { c.Resource = "https://other.example/mcp" }},
		{"invalid issuer", func(c *Credentials) { c.Issuer = "http://issuer.example" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			credentials := storedCredentials("access", "refresh", "client")
			test.mutate(&credentials)
			service, store := recoveryService(t, credentials)
			service.Discover = func(context.Context, *http.Client) (Discovery, error) {
				t.Fatal("invalid stored credentials triggered discovery")
				return Discovery{}, nil
			}
			want := fmt.Sprintf("stored credentials for profile %q are invalid: %v", "acme", validateStoredCredentials(credentials))
			_, accessErr := service.AccessToken(context.Background(), "acme")
			_, refreshErr := service.Refresh(context.Background(), "acme")
			for _, err := range []error{accessErr, refreshErr} {
				if !errors.Is(err, ErrReauthenticationRequired) || err.Error() != want {
					t.Fatalf("error = %v, want unchanged diagnostic %q and reauthentication classification", err, want)
				}
				if errors.Unwrap(err) == nil {
					t.Fatal("classification lost underlying validation error")
				}
			}
			if store.saveCalls != 0 || len(store.deleted) != 0 || store.values["acme"] != credentials {
				t.Fatal("invalid credentials were mutated")
			}
		})
	}
}

func TestMissingRefreshTokenRequiresSignInOnlyWhenRenewalIsNeeded(t *testing.T) {
	credentials := storedCredentials("access", "", "client")
	service, store := recoveryService(t, credentials)
	service.Discover = func(context.Context, *http.Client) (Discovery, error) {
		t.Fatal("missing refresh token triggered discovery")
		return Discovery{}, nil
	}
	_, accessErr := service.AccessToken(context.Background(), "acme")
	_, refreshErr := service.Refresh(context.Background(), "acme")
	for _, err := range []error{accessErr, refreshErr} {
		if !errors.Is(err, ErrReauthenticationRequired) || err.Error() != `profile "acme" has no refresh token; log in again` {
			t.Fatalf("missing refresh-token error = %v", err)
		}
	}
	credentials.ExpiresAt = service.Now().Add(time.Hour)
	store.values["acme"] = credentials
	if token, err := service.AccessToken(context.Background(), "acme"); err != nil || token != "access" {
		t.Fatalf("unexpired access token should remain usable: token=%q error=%v", token, err)
	}
	if store.saveCalls != 0 || len(store.deleted) != 0 {
		t.Fatal("renewal failure changed credential storage")
	}
}

func TestRefreshRejectionClassificationPreservesDiagnosticsAndNeverRetries(t *testing.T) {
	for _, test := range []struct {
		name, code string
		status     int
		wantReauth bool
	}{
		{"revoked grant", "invalid_grant", 400, true},
		{"deleted client", "invalid_client", 401, true},
		{"unauthorized client", "unauthorized_client", 400, true},
		{"unknown code", "custom_provider_error", 400, false},
		{"invalid request", "invalid_request", 400, false},
		{"scope configuration", "invalid_scope", 400, false},
		{"temporary provider error", "temporarily_unavailable", 503, false},
		{"server error despite grant body", "invalid_grant", 503, false},
		{"rate limit despite grant body", "invalid_grant", 429, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			credentials := storedCredentials("access", "refresh", "client")
			service, store := recoveryService(t, credentials)
			var calls int
			service.HTTPClient = &http.Client{Transport: reauthenticationTransport(func(request *http.Request) (*http.Response, error) {
				calls++
				if err := request.ParseForm(); err != nil {
					t.Fatal(err)
				}
				if request.Form.Get("grant_type") != "refresh_token" {
					t.Fatalf("unexpected grant type %q", request.Form.Get("grant_type"))
				}
				body, _ := json.Marshal(tokenResponse{Error: test.code, Description: "provider diagnostic"})
				return &http.Response{StatusCode: test.status, Body: io.NopCloser(strings.NewReader(string(body))), Header: make(http.Header)}, nil
			})}
			_, err := service.AccessToken(context.Background(), "acme")
			want := fmt.Sprintf("refresh OAuth token: token endpoint returned HTTP %d: %s: provider diagnostic", test.status, test.code)
			if err == nil || err.Error() != want || errors.Is(err, ErrReauthenticationRequired) != test.wantReauth {
				t.Fatalf("error = %v, want %q with reauth=%v", err, want, test.wantReauth)
			}
			if calls != 1 || store.saveCalls != 0 || len(store.deleted) != 0 || store.values["acme"] != credentials {
				t.Fatalf("failure retried or changed credentials: requests=%d saves=%d deletes=%v", calls, store.saveCalls, store.deleted)
			}
		})
	}
}

func TestRefreshTransportAndDiscoveryFailuresDoNotRequireSignIn(t *testing.T) {
	for _, cause := range []error{errors.New("temporary network failure"), context.Canceled, context.DeadlineExceeded} {
		t.Run(cause.Error(), func(t *testing.T) {
			service, _ := recoveryService(t, storedCredentials("access", "refresh", "client"))
			service.HTTPClient = &http.Client{Transport: reauthenticationTransport(func(*http.Request) (*http.Response, error) {
				return nil, cause
			})}
			_, err := service.AccessToken(context.Background(), "acme")
			if !errors.Is(err, cause) || errors.Is(err, ErrReauthenticationRequired) {
				t.Fatalf("transport error classification changed: %v", err)
			}
			service.Discover = func(context.Context, *http.Client) (Discovery, error) { return Discovery{}, cause }
			_, err = service.AccessToken(context.Background(), "acme")
			if !errors.Is(err, cause) || errors.Is(err, ErrReauthenticationRequired) {
				t.Fatalf("discovery error classification changed: %v", err)
			}
		})
	}
}
