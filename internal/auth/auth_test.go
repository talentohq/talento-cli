package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/talentohq/talento-cli/internal/config"
)

type memoryCredentials struct {
	mu                     sync.Mutex
	values                 map[string]Credentials
	deleted                []string
	saveCalls              int
	failSaveAt             int
	applyBeforeSaveFailure bool
	saveErr                error
}

func (s *memoryCredentials) Load(profile string) (Credentials, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.values[profile]
	if !ok {
		return Credentials{}, fmt.Errorf("credentials not found")
	}
	return value, nil
}

func (s *memoryCredentials) Save(profile string, value Credentials) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.saveCalls++
	if s.failSaveAt == s.saveCalls && !s.applyBeforeSaveFailure {
		return s.saveError()
	}
	if s.values == nil {
		s.values = make(map[string]Credentials)
	}
	s.values[profile] = value
	if s.failSaveAt == s.saveCalls {
		return s.saveError()
	}
	return nil
}

func (s *memoryCredentials) saveError() error {
	if s.saveErr != nil {
		return s.saveErr
	}
	return errors.New("credential store unavailable")
}

func (s *memoryCredentials) Delete(profile string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.values, profile)
	s.deleted = append(s.deleted, profile)
	return nil
}

func (*memoryCredentials) UsingKeyring() bool { return true }
func (*memoryCredentials) Warning() string    { return "" }

type memoryProfiles struct {
	mu                       sync.Mutex
	values                   map[string]config.Profile
	upsertCalls              int
	failUpsertAt             int
	applyBeforeUpsertFailure bool
	upsertErr                error
}

func (s *memoryProfiles) SnapshotProfile(name string) (config.ProfileSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	profile, ok := s.values[name]
	return config.ProfileSnapshot{Name: name, Profile: profile, Exists: ok}, nil
}

func (s *memoryProfiles) UpsertProfile(profile config.Profile) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.upsertCalls++
	if s.failUpsertAt == s.upsertCalls && !s.applyBeforeUpsertFailure {
		return s.profileError()
	}
	if s.values == nil {
		s.values = make(map[string]config.Profile)
	}
	s.values[profile.Name] = profile
	if s.failUpsertAt == s.upsertCalls {
		return s.profileError()
	}
	return nil
}

func (s *memoryProfiles) RestoreProfile(snapshot config.ProfileSnapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if snapshot.Exists {
		if s.values == nil {
			s.values = make(map[string]config.Profile)
		}
		s.values[snapshot.Name] = snapshot.Profile
	} else {
		delete(s.values, snapshot.Name)
	}
	return nil
}

func (s *memoryProfiles) profileError() error {
	if s.upsertErr != nil {
		return s.upsertErr
	}
	return errors.New("config store unavailable")
}

type revocationRequest struct {
	Token    string
	ClientID string
	Hint     string
}

type loginServer struct {
	server         *httptest.Server
	discovery      Discovery
	mu             sync.Mutex
	registerCalls  int
	revocations    []revocationRequest
	revocationCode int
	revocationBody string
	tokenScope     string
}

func newLoginServer(t *testing.T) *loginServer {
	t.Helper()
	fixture := &loginServer{revocationCode: http.StatusOK, tokenScope: config.Scope}
	mux := http.NewServeMux()
	mux.HandleFunc("/register", func(writer http.ResponseWriter, request *http.Request) {
		var payload struct {
			RedirectURIs []string `json:"redirect_uris"`
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Error(err)
			return
		}
		fixture.mu.Lock()
		fixture.registerCalls++
		clientID := fmt.Sprintf("replacement-client-%d", fixture.registerCalls)
		fixture.mu.Unlock()
		_ = json.NewEncoder(writer).Encode(Registration{
			ClientID: clientID, RedirectURIs: payload.RedirectURIs,
			GrantTypes: []string{"authorization_code", "refresh_token"}, ResponseTypes: []string{"code"},
			TokenEndpointAuthMethod: "none", Scope: config.Scope,
		})
	})
	mux.HandleFunc("/token", func(writer http.ResponseWriter, request *http.Request) {
		if err := request.ParseForm(); err != nil {
			t.Error(err)
			return
		}
		clientID := request.Form.Get("client_id")
		fixture.mu.Lock()
		tokenScope := fixture.tokenScope
		fixture.mu.Unlock()
		_ = json.NewEncoder(writer).Encode(tokenResponse{
			AccessToken: "access-for-" + clientID, RefreshToken: "refresh-for-" + clientID,
			TokenType: "Bearer", ExpiresIn: 3600, Scope: tokenScope,
		})
	})
	mux.HandleFunc("/revoke", func(writer http.ResponseWriter, request *http.Request) {
		if err := request.ParseForm(); err != nil {
			t.Error(err)
			return
		}
		fixture.mu.Lock()
		fixture.revocations = append(fixture.revocations, revocationRequest{
			Token: request.Form.Get("token"), ClientID: request.Form.Get("client_id"), Hint: request.Form.Get("token_type_hint"),
		})
		code := fixture.revocationCode
		body := fixture.revocationBody
		fixture.mu.Unlock()
		writer.WriteHeader(code)
		_, _ = writer.Write([]byte(body))
	})
	fixture.server = httptest.NewServer(mux)
	fixture.discovery = Discovery{
		Resource: ProtectedResourceMetadata{Resource: config.Endpoint},
		Authorization: AuthorizationServerMetadata{
			Issuer: "https://issuer.example", AuthorizationEndpoint: fixture.server.URL + "/authorize",
			TokenEndpoint: fixture.server.URL + "/token", RevocationEndpoint: fixture.server.URL + "/revoke",
			RegistrationEndpoint: fixture.server.URL + "/register",
		},
	}
	t.Cleanup(fixture.server.Close)
	return fixture
}

func (s *loginServer) requests() []revocationRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]revocationRequest(nil), s.revocations...)
}

func configureLoginService(t *testing.T, service *Service, fixture *loginServer) {
	t.Helper()
	service.Now = func() time.Time { return time.Date(2026, 8, 25, 10, 0, 0, 0, time.UTC) }
	service.Discover = func(context.Context, *http.Client) (Discovery, error) { return fixture.discovery, nil }
	service.OpenBrowser = func(authorizationURL string) error {
		u, err := url.Parse(authorizationURL)
		if err != nil {
			return err
		}
		callback := u.Query().Get("redirect_uri") + "?code=authorization-code&state=" + url.QueryEscape(u.Query().Get("state"))
		go func() {
			response, callbackErr := http.Get(callback)
			if callbackErr == nil {
				_ = response.Body.Close()
			}
		}()
		return nil
	}
}

func storedCredentials(accessToken, refreshToken, clientID string) Credentials {
	return Credentials{
		AccessToken: accessToken, RefreshToken: refreshToken, TokenType: "Bearer", Scope: config.Scope,
		ExpiresAt: time.Date(2026, 8, 25, 11, 0, 0, 0, time.UTC), ClientID: clientID,
		Issuer: "https://issuer.example", Resource: config.Endpoint,
	}
}

func TestOAuthLoginRefreshAndRevocation(t *testing.T) {
	var server *httptest.Server
	var registeredRedirect string
	var refreshCalls, revokeCalls int
	mux := http.NewServeMux()
	mux.HandleFunc("/register", func(writer http.ResponseWriter, request *http.Request) {
		var payload struct {
			RedirectURIs []string `json:"redirect_uris"`
			Scope        string   `json:"scope"`
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		registeredRedirect = payload.RedirectURIs[0]
		_ = json.NewEncoder(writer).Encode(Registration{
			ClientID: "public-client", RedirectURIs: payload.RedirectURIs,
			GrantTypes: []string{"authorization_code", "refresh_token"}, ResponseTypes: []string{"code"},
			TokenEndpointAuthMethod: "none", Scope: config.Scope,
		})
	})
	mux.HandleFunc("/token", func(writer http.ResponseWriter, request *http.Request) {
		if err := request.ParseForm(); err != nil {
			t.Error(err)
		}
		if request.Form.Get("resource") != config.Endpoint {
			t.Errorf("resource = %q", request.Form.Get("resource"))
		}
		access := "access-one"
		if request.Form.Get("grant_type") == "refresh_token" {
			refreshCalls++
			access = "access-two"
		}
		_ = json.NewEncoder(writer).Encode(tokenResponse{AccessToken: access, RefreshToken: "refresh", TokenType: "Bearer", ExpiresIn: 3600, Scope: config.Scope})
	})
	mux.HandleFunc("/revoke", func(writer http.ResponseWriter, request *http.Request) {
		revokeCalls++
		writer.WriteHeader(http.StatusOK)
	})
	server = httptest.NewServer(mux)
	defer server.Close()

	discovery := Discovery{
		Resource: ProtectedResourceMetadata{Resource: config.Endpoint, AuthorizationServers: []string{server.URL}, ScopesSupported: []string{config.Scope}},
		Authorization: AuthorizationServerMetadata{
			Issuer: "https://issuer.example", AuthorizationEndpoint: server.URL + "/authorize", TokenEndpoint: server.URL + "/token",
			RevocationEndpoint: server.URL + "/revoke", RegistrationEndpoint: server.URL + "/register",
			ScopesSupported: []string{config.Scope}, GrantTypesSupported: []string{"authorization_code", "refresh_token"},
			ResponseTypesSupported: []string{"code"}, TokenEndpointAuthMethodsSupported: []string{"none"}, CodeChallengeMethodsSupported: []string{"S256"},
		},
	}
	store := config.NewStore(filepath.Join(t.TempDir(), "config.json"))
	if _, err := store.CreateProfile("acme"); err != nil {
		t.Fatal(err)
	}
	credentials := &memoryCredentials{values: make(map[string]Credentials)}
	now := time.Date(2026, 8, 25, 10, 0, 0, 0, time.UTC)
	service := NewService(store, credentials)
	service.Now = func() time.Time { return now }
	service.Discover = func(context.Context, *http.Client) (Discovery, error) { return discovery, nil }
	service.OpenBrowser = func(authorizationURL string) error {
		u, err := url.Parse(authorizationURL)
		if err != nil {
			return err
		}
		if u.Query().Get("code_challenge_method") != "S256" || u.Query().Get("resource") != config.Endpoint {
			t.Errorf("authorization URL = %s", authorizationURL)
		}
		callback := u.Query().Get("redirect_uri") + "?code=authorization-code&state=" + url.QueryEscape(u.Query().Get("state"))
		go func() {
			response, callbackErr := http.Get(callback)
			if callbackErr == nil {
				_ = response.Body.Close()
			}
		}()
		return nil
	}
	status, err := service.Login(context.Background(), LoginOptions{Profile: "acme"})
	if err != nil {
		t.Fatal(err)
	}
	if !status.Authenticated || status.Scope != config.Scope || registeredRedirect == "" {
		t.Fatalf("status = %#v, redirect = %q", status, registeredRedirect)
	}
	stored, err := credentials.Load("acme")
	if err != nil || stored.AccessToken != "access-one" || stored.Resource != config.Endpoint {
		t.Fatalf("credentials = %#v, err = %v", stored, err)
	}
	now = now.Add(2 * time.Hour)
	access, err := service.AccessToken(context.Background(), "acme")
	if err != nil || access != "access-two" || refreshCalls != 1 {
		t.Fatalf("access = %q, refresh calls = %d, err = %v", access, refreshCalls, err)
	}
	logout, err := service.Logout(context.Background(), "acme")
	if err != nil || !logout.Revoked || revokeCalls != 1 {
		t.Fatalf("logout = %#v, revoke calls = %d, err = %v", logout, revokeCalls, err)
	}
	if _, err := credentials.Load("acme"); !IsMissingCredentials(err) {
		t.Fatalf("credentials were not deleted: %v", err)
	}
}

func TestLoginRevokesSupersededGrantAfterInstallingReplacement(t *testing.T) {
	fixture := newLoginServer(t)
	oldCredentials := storedCredentials("old-access-secret", "old-refresh-secret", "old-client")
	credentials := &memoryCredentials{values: map[string]Credentials{"acme": oldCredentials}}
	store := config.NewStore(filepath.Join(t.TempDir(), "config.json"))
	profile, err := store.CreateProfile("acme")
	if err != nil {
		t.Fatal(err)
	}
	profile.ClientID = "old-client"
	profile.RedirectURI = "http://127.0.0.1:1234/callback"
	if err := store.UpsertProfile(profile); err != nil {
		t.Fatal(err)
	}
	service := NewService(store, credentials)
	configureLoginService(t, service, fixture)

	status, err := service.Login(context.Background(), LoginOptions{Profile: "acme"})
	if err != nil {
		t.Fatal(err)
	}
	if !status.Authenticated || status.Warning != "" {
		t.Fatalf("status = %#v", status)
	}
	requests := fixture.requests()
	if len(requests) != 1 || requests[0] != (revocationRequest{Token: "old-refresh-secret", ClientID: "old-client", Hint: "refresh_token"}) {
		t.Fatalf("revocations = %#v", requests)
	}
	replacement, err := credentials.Load("acme")
	if err != nil {
		t.Fatal(err)
	}
	if replacement.RefreshToken != "refresh-for-replacement-client-1" || replacement.ClientID != "replacement-client-1" {
		t.Fatalf("replacement credentials = %#v", replacement)
	}
	updatedProfile, err := store.Profile("acme")
	if err != nil {
		t.Fatal(err)
	}
	if updatedProfile.ClientID != "replacement-client-1" || updatedProfile.RedirectURI == profile.RedirectURI {
		t.Fatalf("updated profile = %#v", updatedProfile)
	}
}

func TestLoginCreatesFirstLocalGrantWithoutRollbackState(t *testing.T) {
	fixture := newLoginServer(t)
	credentials := &memoryCredentials{values: make(map[string]Credentials)}
	store := config.NewStore(filepath.Join(t.TempDir(), "config.json"))
	service := NewService(store, credentials)
	configureLoginService(t, service, fixture)

	status, err := service.Login(context.Background(), LoginOptions{Profile: "acme"})
	if err != nil {
		t.Fatal(err)
	}
	if !status.Authenticated || status.Warning != "" {
		t.Fatalf("status = %#v", status)
	}
	if requests := fixture.requests(); len(requests) != 0 {
		t.Fatalf("first login revoked a nonexistent grant: %#v", requests)
	}
	stored, loadErr := credentials.Load("acme")
	if loadErr != nil || stored.ClientID != "replacement-client-1" {
		t.Fatalf("credentials = %#v, err = %v", stored, loadErr)
	}
	profile, profileErr := store.Profile("acme")
	if profileErr != nil || profile.ClientID != "replacement-client-1" {
		t.Fatalf("profile = %#v, err = %v", profile, profileErr)
	}
}

func TestLoginRevokesReplacementAndRestoresCredentialsWhenCredentialSaveFails(t *testing.T) {
	fixture := newLoginServer(t)
	oldCredentials := storedCredentials("old-access-secret", "old-refresh-secret", "old-client")
	credentials := &memoryCredentials{
		values: map[string]Credentials{"acme": oldCredentials}, failSaveAt: 1, applyBeforeSaveFailure: true,
	}
	store := config.NewStore(filepath.Join(t.TempDir(), "config.json"))
	profile, err := store.CreateProfile("acme")
	if err != nil {
		t.Fatal(err)
	}
	profile.ClientID = "old-client"
	if err := store.UpsertProfile(profile); err != nil {
		t.Fatal(err)
	}
	service := NewService(store, credentials)
	configureLoginService(t, service, fixture)

	_, err = service.Login(context.Background(), LoginOptions{Profile: "acme"})
	if err == nil || !strings.Contains(err.Error(), "store OAuth credentials") {
		t.Fatalf("expected credential persistence failure, got %v", err)
	}
	stored, loadErr := credentials.Load("acme")
	if loadErr != nil || stored != oldCredentials {
		t.Fatalf("credentials = %#v, err = %v", stored, loadErr)
	}
	unchangedProfile, profileErr := store.Profile("acme")
	if profileErr != nil || unchangedProfile.ClientID != "old-client" {
		t.Fatalf("profile = %#v, err = %v", unchangedProfile, profileErr)
	}
	requests := fixture.requests()
	if len(requests) != 1 || requests[0].Token != "refresh-for-replacement-client-1" || requests[0].ClientID != "replacement-client-1" {
		t.Fatalf("revocations = %#v", requests)
	}
}

func TestLoginRevokesReplacementAndRestoresStateWhenProfileSaveFails(t *testing.T) {
	fixture := newLoginServer(t)
	oldCredentials := storedCredentials("old-access-secret", "old-refresh-secret", "old-client")
	credentials := &memoryCredentials{values: map[string]Credentials{"acme": oldCredentials}}
	oldProfile := config.Profile{
		Name: "acme", Endpoint: config.Endpoint, ClientID: "old-client", RedirectURI: "http://127.0.0.1:1234/callback",
		RegistrationScope: config.Scope, CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	profiles := &memoryProfiles{
		values: map[string]config.Profile{"acme": oldProfile}, failUpsertAt: 1, applyBeforeUpsertFailure: true,
	}
	service := NewService(config.NewStore(filepath.Join(t.TempDir(), "unused.json")), credentials)
	service.Config = profiles
	configureLoginService(t, service, fixture)

	_, err := service.Login(context.Background(), LoginOptions{Profile: "acme"})
	if err == nil || !strings.Contains(err.Error(), "store profile metadata") {
		t.Fatalf("expected profile persistence failure, got %v", err)
	}
	stored, loadErr := credentials.Load("acme")
	if loadErr != nil || stored != oldCredentials {
		t.Fatalf("credentials = %#v, err = %v", stored, loadErr)
	}
	restoredProfile, profileErr := profiles.SnapshotProfile("acme")
	if profileErr != nil || !restoredProfile.Exists || restoredProfile.Profile != oldProfile {
		t.Fatalf("profile = %#v, err = %v", restoredProfile, profileErr)
	}
	requests := fixture.requests()
	if len(requests) != 1 || requests[0].Token != "refresh-for-replacement-client-1" || requests[0].ClientID != "replacement-client-1" {
		t.Fatalf("revocations = %#v", requests)
	}
}

func TestLoginKeepsReplacementAndWarnsWhenSupersededRevocationFails(t *testing.T) {
	fixture := newLoginServer(t)
	fixture.revocationCode = http.StatusInternalServerError
	fixture.revocationBody = "old-refresh-secret replacement-refresh-secret"
	oldCredentials := storedCredentials("old-access-secret", "old-refresh-secret", "old-client")
	credentials := &memoryCredentials{values: map[string]Credentials{"acme": oldCredentials}}
	store := config.NewStore(filepath.Join(t.TempDir(), "config.json"))
	if _, err := store.CreateProfile("acme"); err != nil {
		t.Fatal(err)
	}
	service := NewService(store, credentials)
	configureLoginService(t, service, fixture)

	status, err := service.Login(context.Background(), LoginOptions{Profile: "acme"})
	if err != nil {
		t.Fatalf("replacement login should remain successful: %v", err)
	}
	if !strings.Contains(status.Warning, "previous server grant could not be revoked") || !strings.Contains(status.Warning, "HTTP 500") {
		t.Fatalf("warning = %q", status.Warning)
	}
	for _, secret := range []string{"old-access-secret", "old-refresh-secret", "replacement-refresh-secret", "refresh-for-replacement-client-1"} {
		if strings.Contains(status.Warning, secret) {
			t.Fatalf("warning leaked secret %q: %s", secret, status.Warning)
		}
	}
	replacement, loadErr := credentials.Load("acme")
	if loadErr != nil || replacement.ClientID != "replacement-client-1" {
		t.Fatalf("credentials = %#v, err = %v", replacement, loadErr)
	}
	updatedProfile, profileErr := store.Profile("acme")
	if profileErr != nil || updatedProfile.ClientID != "replacement-client-1" {
		t.Fatalf("profile = %#v, err = %v", updatedProfile, profileErr)
	}
}

func TestLoginRevokesLegacyGrantUsingAccessToken(t *testing.T) {
	fixture := newLoginServer(t)
	legacyCredentials := storedCredentials("legacy-access-secret", "", "legacy-client")
	credentials := &memoryCredentials{values: map[string]Credentials{"acme": legacyCredentials}}
	store := config.NewStore(filepath.Join(t.TempDir(), "config.json"))
	if _, err := store.CreateProfile("acme"); err != nil {
		t.Fatal(err)
	}
	service := NewService(store, credentials)
	configureLoginService(t, service, fixture)

	if _, err := service.Login(context.Background(), LoginOptions{Profile: "acme"}); err != nil {
		t.Fatal(err)
	}
	requests := fixture.requests()
	if len(requests) != 1 || requests[0] != (revocationRequest{Token: "legacy-access-secret", ClientID: "legacy-client", Hint: "access_token"}) {
		t.Fatalf("revocations = %#v", requests)
	}
}

func TestLoginRevokesInvalidIssuedGrantWithoutLeakingTokens(t *testing.T) {
	fixture := newLoginServer(t)
	fixture.tokenScope = "wrong:scope"
	fixture.revocationCode = http.StatusInternalServerError
	fixture.revocationBody = "refresh-for-replacement-client-1"
	credentials := &memoryCredentials{values: make(map[string]Credentials)}
	store := config.NewStore(filepath.Join(t.TempDir(), "config.json"))
	service := NewService(store, credentials)
	configureLoginService(t, service, fixture)

	_, err := service.Login(context.Background(), LoginOptions{Profile: "acme"})
	if err == nil || !strings.Contains(err.Error(), "required scope") || !strings.Contains(err.Error(), "HTTP 500") {
		t.Fatalf("expected validation and cleanup failure, got %v", err)
	}
	if strings.Contains(err.Error(), "refresh-for-replacement-client-1") || strings.Contains(err.Error(), "access-for-replacement-client-1") {
		t.Fatalf("login error leaked token material: %v", err)
	}
	requests := fixture.requests()
	if len(requests) != 1 || requests[0].Token != "refresh-for-replacement-client-1" {
		t.Fatalf("revocations = %#v", requests)
	}
	if _, loadErr := credentials.Load("acme"); !IsMissingCredentials(loadErr) {
		t.Fatalf("invalid credentials were stored: %v", loadErr)
	}
}

func TestOAuthCallbackRejectsWrongState(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/register" {
			var payload struct {
				RedirectURIs []string `json:"redirect_uris"`
			}
			_ = json.NewDecoder(request.Body).Decode(&payload)
			_ = json.NewEncoder(writer).Encode(Registration{ClientID: "client", RedirectURIs: payload.RedirectURIs, TokenEndpointAuthMethod: "none", Scope: config.Scope})
			return
		}
		writer.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	discovery := Discovery{Resource: ProtectedResourceMetadata{Resource: config.Endpoint}, Authorization: AuthorizationServerMetadata{
		Issuer: server.URL, AuthorizationEndpoint: server.URL + "/authorize", TokenEndpoint: server.URL + "/token", RegistrationEndpoint: server.URL + "/register",
	}}
	store := config.NewStore(filepath.Join(t.TempDir(), "config.json"))
	_, _ = store.CreateProfile("acme")
	service := NewService(store, &memoryCredentials{values: make(map[string]Credentials)})
	service.Discover = func(context.Context, *http.Client) (Discovery, error) { return discovery, nil }
	service.OpenBrowser = func(authorizationURL string) error {
		u, _ := url.Parse(authorizationURL)
		callback := u.Query().Get("redirect_uri") + "?code=code&state=wrong"
		go func() {
			response, _ := http.Get(callback)
			if response != nil {
				_ = response.Body.Close()
			}
		}()
		return nil
	}
	_, err := service.Login(context.Background(), LoginOptions{Profile: "acme"})
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "state") {
		t.Fatalf("expected state rejection, got %v", err)
	}
}

func TestDiscoverValidatesIssuerResourceScopeAndPKCE(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/resource":
			_ = json.NewEncoder(writer).Encode(ProtectedResourceMetadata{Resource: server.URL + "/mcp", AuthorizationServers: []string{server.URL}, ScopesSupported: []string{config.Scope}})
		case "/.well-known/oauth-authorization-server":
			_ = json.NewEncoder(writer).Encode(AuthorizationServerMetadata{
				Issuer: server.URL, AuthorizationEndpoint: server.URL + "/authorize", TokenEndpoint: server.URL + "/token", RegistrationEndpoint: server.URL + "/register",
				ScopesSupported: []string{config.Scope}, GrantTypesSupported: []string{"authorization_code", "refresh_token"}, ResponseTypesSupported: []string{"code"},
				TokenEndpointAuthMethodsSupported: []string{"none"}, CodeChallengeMethodsSupported: []string{"S256"},
			})
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	discovery, err := DiscoverFrom(context.Background(), server.Client(), server.URL+"/resource", server.URL+"/mcp")
	if err != nil {
		t.Fatal(err)
	}
	if discovery.Authorization.Issuer != server.URL {
		t.Fatalf("issuer = %q", discovery.Authorization.Issuer)
	}
	_, err = DiscoverFrom(context.Background(), server.Client(), server.URL+"/resource", server.URL+"/other")
	if err == nil || !strings.Contains(err.Error(), "resource mismatch") {
		t.Fatalf("expected resource mismatch, got %v", err)
	}
}

func TestStatusNeverSerializesCredentialMaterial(t *testing.T) {
	credentials := &memoryCredentials{values: map[string]Credentials{
		"acme": {
			AccessToken: "access-secret", RefreshToken: "refresh-secret", ClientSecret: "client-secret",
			Scope: config.Scope, ExpiresAt: time.Now().Add(time.Hour), Issuer: "https://issuer.example", Resource: config.Endpoint,
		},
	}}
	service := NewService(config.NewStore(filepath.Join(t.TempDir(), "config.json")), credentials)
	status, err := service.Status("acme")
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"access-secret", "refresh-secret", "client-secret"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("status leaked %s: %s", secret, encoded)
		}
	}
}

func TestBrowserAuthorizationErrorExplainsTimeoutAndCancellation(t *testing.T) {
	timedOut := browserAuthorizationError(context.DeadlineExceeded)
	if !strings.Contains(timedOut.Error(), "timed out after 5 minutes") || !strings.Contains(timedOut.Error(), "talento auth login") {
		t.Fatalf("timeout error = %v", timedOut)
	}

	canceled := browserAuthorizationError(context.Canceled)
	if !strings.Contains(canceled.Error(), "canceled while waiting for browser authorization") {
		t.Fatalf("cancellation error = %v", canceled)
	}

	callbackFailure := errors.New("state mismatch")
	wrapped := browserAuthorizationError(callbackFailure)
	if !errors.Is(wrapped, callbackFailure) || !strings.Contains(wrapped.Error(), "wait for browser authorization") {
		t.Fatalf("callback error = %v", wrapped)
	}
}
