package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/basecamp/cli/oauthcallback"
	"github.com/basecamp/cli/pkce"
	"github.com/talentohq/talento-cli/internal/config"
)

type Service struct {
	Config      ProfileStore
	Credentials CredentialStore
	HTTPClient  *http.Client
	OpenBrowser func(string) error
	Now         func() time.Time
	Discover    func(context.Context, *http.Client) (Discovery, error)
}

// ErrReauthenticationRequired identifies an unusable grant that needs explicit
// user sign-in. Network failures and transient provider errors do not match it.
var ErrReauthenticationRequired = errors.New("reauthentication is required")

// Keep existing CLI diagnostics unchanged while exposing recovery semantics to
// long-lived clients. The underlying error remains available through Unwrap.
type reauthenticationError struct{ cause error }

func (e *reauthenticationError) Error() string { return e.cause.Error() }
func (e *reauthenticationError) Unwrap() error { return e.cause }
func (e *reauthenticationError) Is(target error) bool {
	return target == ErrReauthenticationRequired
}

type ProfileStore interface {
	SnapshotProfile(string) (config.ProfileSnapshot, error)
	UpsertProfile(config.Profile) error
	RestoreProfile(config.ProfileSnapshot) error
}

type LoginOptions struct {
	Profile string
	NoOpen  bool
	URLSink func(string)
}

type Registration struct {
	ClientID                string   `json:"client_id"`
	ClientSecret            string   `json:"client_secret,omitempty"`
	ClientIDIssuedAt        int64    `json:"client_id_issued_at"`
	ClientSecretExpiresAt   int64    `json:"client_secret_expires_at,omitempty"`
	ClientName              string   `json:"client_name"`
	RedirectURIs            []string `json:"redirect_uris"`
	GrantTypes              []string `json:"grant_types"`
	ResponseTypes           []string `json:"response_types"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
	Scope                   string   `json:"scope"`
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
	Scope        string `json:"scope"`
	Error        string `json:"error"`
	Description  string `json:"error_description"`
}

type Status struct {
	Profile       string    `json:"profile"`
	Authenticated bool      `json:"authenticated"`
	ExpiresAt     time.Time `json:"expires_at,omitempty"`
	Expired       bool      `json:"expired,omitempty"`
	Scope         string    `json:"scope,omitempty"`
	Issuer        string    `json:"issuer,omitempty"`
	Resource      string    `json:"resource,omitempty"`
	Storage       string    `json:"storage,omitempty"`
	Warning       string    `json:"warning,omitempty"`
}

type LogoutResult struct {
	Profile string `json:"profile"`
	Revoked bool   `json:"revoked"`
	Warning string `json:"warning,omitempty"`
}

func NewService(cfg *config.Store, credentials CredentialStore) *Service {
	return &Service{
		Config:      cfg,
		Credentials: credentials,
		HTTPClient:  &http.Client{Timeout: 30 * time.Second},
		OpenBrowser: openBrowser,
		Now:         time.Now,
		Discover:    Discover,
	}
}

func (s *Service) Login(ctx context.Context, opts LoginOptions) (Status, error) {
	if opts.Profile == "" {
		return Status{}, fmt.Errorf("profile is required")
	}
	previous, err := s.captureLoginState(opts.Profile)
	if err != nil {
		return Status{}, err
	}
	discovery, err := s.Discover(ctx, s.HTTPClient)
	if err != nil {
		return Status{}, err
	}

	lc := net.ListenConfig{}
	listener, err := lc.Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		return Status{}, fmt.Errorf("start OAuth callback listener: %w", err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	redirectURI := "http://127.0.0.1:" + strconv.Itoa(port) + "/callback"

	registration, err := s.register(ctx, discovery.Authorization, redirectURI)
	if err != nil {
		return Status{}, err
	}
	if registration.TokenEndpointAuthMethod != "none" {
		return Status{}, fmt.Errorf("dynamic registration returned unsupported token authentication method %q", registration.TokenEndpointAuthMethod)
	}
	if !contains(registration.RedirectURIs, redirectURI) {
		return Status{}, fmt.Errorf("dynamic registration did not preserve the callback URI")
	}
	if !scopeContains(registration.Scope, config.Scope) {
		return Status{}, fmt.Errorf("dynamic registration did not grant scope %q", config.Scope)
	}

	verifier := pkce.GenerateVerifier()
	state := pkce.GenerateState()
	authorizationURL, err := buildAuthorizationURL(discovery.Authorization.AuthorizationEndpoint, registration.ClientID, redirectURI, state, verifier)
	if err != nil {
		return Status{}, err
	}
	callbackCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	type callbackResult struct {
		code string
		err  error
	}
	callback := make(chan callbackResult, 1)
	go func() {
		code, callbackErr := oauthcallback.WaitForCallback(callbackCtx, state, listener, "")
		callback <- callbackResult{code: code, err: callbackErr}
	}()

	if opts.URLSink != nil {
		opts.URLSink(authorizationURL)
	}
	if !opts.NoOpen {
		if err := s.OpenBrowser(authorizationURL); err != nil {
			return Status{}, fmt.Errorf("open browser: %w (retry with --no-open)", err)
		}
	}
	result := <-callback
	if result.err != nil {
		return Status{}, result.err
	}

	token, err := s.exchangeCode(ctx, discovery.Authorization, registration, redirectURI, verifier, result.code)
	if err != nil {
		return Status{}, err
	}
	credentials, err := validateToken(discovery, registration, token, s.Now())
	if err != nil {
		cleanupErr := s.revokeIssuedToken(ctx, discovery.Authorization.RevocationEndpoint, registration, token)
		return Status{}, loginFailure(err, cleanupErr, nil)
	}
	if err := s.Credentials.Save(opts.Profile, credentials); err != nil {
		cause := fmt.Errorf("store OAuth credentials: %w", err)
		return Status{}, s.rollbackLogin(ctx, discovery.Authorization.RevocationEndpoint, opts.Profile, credentials, previous, false, cause)
	}

	profile := previous.profile.Profile
	if !previous.profile.Exists {
		profile = config.Profile{Name: opts.Profile}
	}
	profile.ClientID = registration.ClientID
	profile.RedirectURI = redirectURI
	if err := s.Config.UpsertProfile(profile); err != nil {
		cause := fmt.Errorf("store profile metadata: %w", err)
		return Status{}, s.rollbackLogin(ctx, discovery.Authorization.RevocationEndpoint, opts.Profile, credentials, previous, true, cause)
	}

	status := s.statusFromCredentials(opts.Profile, credentials)
	if previous.hasCredentials {
		if discovery.Authorization.RevocationEndpoint == "" {
			status.Warning = appendWarning(status.Warning, "The new login is active, but the authorization server did not advertise a revocation endpoint for the previous grant.")
		} else if err := s.revokeWithCleanupContext(ctx, discovery.Authorization.RevocationEndpoint, previous.credentials); err != nil {
			status.Warning = appendWarning(status.Warning, "The new login is active, but the previous server grant could not be revoked: "+err.Error())
		}
	}
	return status, nil
}

type loginState struct {
	credentials    Credentials
	hasCredentials bool
	profile        config.ProfileSnapshot
}

func (s *Service) captureLoginState(profileName string) (loginState, error) {
	var state loginState
	credentials, err := s.Credentials.Load(profileName)
	if err == nil {
		state.credentials = credentials
		state.hasCredentials = true
	} else if !IsMissingCredentials(err) {
		return loginState{}, fmt.Errorf("load existing OAuth credentials: %w", err)
	}

	profile, err := s.Config.SnapshotProfile(profileName)
	if err != nil {
		return loginState{}, fmt.Errorf("load existing profile metadata: %w", err)
	}
	state.profile = profile
	return state, nil
}

func (s *Service) rollbackLogin(ctx context.Context, revocationEndpoint, profileName string, issued Credentials, previous loginState, restoreProfile bool, cause error) error {
	cleanupErr := s.revokeWithCleanupContext(ctx, revocationEndpoint, issued)
	var restoreErrs []error
	if previous.hasCredentials {
		if err := s.Credentials.Save(profileName, previous.credentials); err != nil {
			restoreErrs = append(restoreErrs, fmt.Errorf("restore previous OAuth credentials: %w", err))
		}
	} else if err := s.Credentials.Delete(profileName); err != nil {
		restoreErrs = append(restoreErrs, fmt.Errorf("remove replacement OAuth credentials: %w", err))
	}
	if restoreProfile {
		if err := s.Config.RestoreProfile(previous.profile); err != nil {
			restoreErrs = append(restoreErrs, fmt.Errorf("restore previous profile metadata: %w", err))
		}
	}
	return loginFailure(cause, cleanupErr, errors.Join(restoreErrs...))
}

func (s *Service) revokeIssuedToken(ctx context.Context, endpoint string, registration Registration, token tokenResponse) error {
	credentials := Credentials{
		AccessToken:  token.AccessToken,
		RefreshToken: token.RefreshToken,
		ClientID:     registration.ClientID,
	}
	return s.revokeWithCleanupContext(ctx, endpoint, credentials)
}

func (s *Service) revokeWithCleanupContext(ctx context.Context, endpoint string, credentials Credentials) error {
	if endpoint == "" {
		return fmt.Errorf("authorization server did not advertise a revocation endpoint")
	}
	if credentials.RefreshToken == "" && credentials.AccessToken == "" {
		return fmt.Errorf("grant did not include a revocable token")
	}
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	return s.revoke(cleanupCtx, endpoint, credentials)
}

func loginFailure(cause, cleanupErr, restoreErr error) error {
	errs := []error{cause}
	if cleanupErr != nil {
		errs = append(errs, fmt.Errorf("revoke replacement server grant: %w", cleanupErr))
	}
	if restoreErr != nil {
		errs = append(errs, restoreErr)
	}
	return errors.Join(errs...)
}

func appendWarning(existing, warning string) string {
	if existing == "" {
		return warning
	}
	return existing + " " + warning
}

func (s *Service) AccessToken(ctx context.Context, profile string) (string, error) {
	credentials, err := s.Credentials.Load(profile)
	if err != nil {
		return "", err
	}
	if err := validateStoredCredentials(credentials); err != nil {
		return "", &reauthenticationError{cause: fmt.Errorf("stored credentials for profile %q are invalid: %w", profile, err)}
	}
	if credentials.ExpiresAt.After(s.Now().Add(60 * time.Second)) {
		return credentials.AccessToken, nil
	}
	refreshed, err := s.refresh(ctx, profile, credentials)
	if err != nil {
		return "", err
	}
	return refreshed.AccessToken, nil
}

func (s *Service) Refresh(ctx context.Context, profile string) (Status, error) {
	credentials, err := s.Credentials.Load(profile)
	if err != nil {
		return Status{}, err
	}
	if err := validateStoredCredentials(credentials); err != nil {
		return Status{}, &reauthenticationError{cause: fmt.Errorf("stored credentials for profile %q are invalid: %w", profile, err)}
	}
	if _, err := s.refresh(ctx, profile, credentials); err != nil {
		return Status{}, err
	}
	return s.Status(profile)
}

func validateStoredCredentials(credentials Credentials) error {
	if credentials.AccessToken == "" {
		return fmt.Errorf("access token is missing")
	}
	if !strings.EqualFold(credentials.TokenType, "Bearer") {
		return fmt.Errorf("unsupported token type %q", credentials.TokenType)
	}
	if !scopeContains(credentials.Scope, config.Scope) {
		return fmt.Errorf("required scope %q is missing", config.Scope)
	}
	if normalizeURL(credentials.Resource) != normalizeURL(config.Endpoint) {
		return fmt.Errorf("protected resource mismatch")
	}
	if err := validateHTTPSURL(credentials.Issuer); err != nil {
		return fmt.Errorf("invalid issuer: %w", err)
	}
	return nil
}

func (s *Service) refresh(ctx context.Context, profile string, credentials Credentials) (Credentials, error) {
	if credentials.RefreshToken == "" {
		return Credentials{}, &reauthenticationError{cause: fmt.Errorf("profile %q has no refresh token; log in again", profile)}
	}
	discovery, err := s.Discover(ctx, s.HTTPClient)
	if err != nil {
		return Credentials{}, err
	}
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {credentials.RefreshToken},
		"client_id":     {credentials.ClientID},
		"scope":         {config.Scope},
		"resource":      {config.Endpoint},
	}
	token, err := s.postToken(ctx, discovery.Authorization.TokenEndpoint, form)
	if err != nil {
		return Credentials{}, fmt.Errorf("refresh OAuth token: %w", err)
	}
	if token.RefreshToken == "" {
		token.RefreshToken = credentials.RefreshToken
	}
	registration := Registration{ClientID: credentials.ClientID, ClientSecret: credentials.ClientSecret, Scope: config.Scope}
	refreshed, err := validateToken(discovery, registration, token, s.Now())
	if err != nil {
		return Credentials{}, err
	}
	if err := s.Credentials.Save(profile, refreshed); err != nil {
		return Credentials{}, fmt.Errorf("store refreshed credentials: %w", err)
	}
	return refreshed, nil
}

func (s *Service) Status(profile string) (Status, error) {
	credentials, err := s.Credentials.Load(profile)
	if err != nil {
		if IsMissingCredentials(err) {
			return Status{Profile: profile, Authenticated: false}, nil
		}
		return Status{}, err
	}
	return s.statusFromCredentials(profile, credentials), nil
}

func (s *Service) statusFromCredentials(profile string, credentials Credentials) Status {
	storage := "file"
	if s.Credentials.UsingKeyring() {
		storage = "system"
	}
	return Status{
		Profile:       profile,
		Authenticated: true,
		ExpiresAt:     credentials.ExpiresAt,
		Expired:       !credentials.ExpiresAt.After(s.Now()),
		Scope:         credentials.Scope,
		Issuer:        credentials.Issuer,
		Resource:      credentials.Resource,
		Storage:       storage,
		Warning:       s.Credentials.Warning(),
	}
}

func (s *Service) Logout(ctx context.Context, profile string) (LogoutResult, error) {
	credentials, err := s.Credentials.Load(profile)
	if err != nil {
		if IsMissingCredentials(err) {
			return LogoutResult{Profile: profile}, nil
		}
		return LogoutResult{}, err
	}
	result := LogoutResult{Profile: profile}
	discovery, discoverErr := s.Discover(ctx, s.HTTPClient)
	if discoverErr == nil && discovery.Authorization.RevocationEndpoint != "" {
		if err := s.revoke(ctx, discovery.Authorization.RevocationEndpoint, credentials); err != nil {
			result.Warning = "The local grant was removed, but server revocation failed: " + err.Error()
		} else {
			result.Revoked = true
		}
	} else if discoverErr != nil {
		result.Warning = "The local grant was removed, but OAuth discovery failed before revocation: " + discoverErr.Error()
	}
	if err := s.Credentials.Delete(profile); err != nil {
		return LogoutResult{}, fmt.Errorf("delete local credentials: %w", err)
	}
	return result, nil
}

func (s *Service) register(ctx context.Context, metadata AuthorizationServerMetadata, redirectURI string) (Registration, error) {
	payload := map[string]any{
		"client_name":                "Talento CLI",
		"redirect_uris":              []string{redirectURI},
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
		"token_endpoint_auth_method": "none",
		"scope":                      config.Scope,
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, metadata.RegistrationEndpoint, bytes.NewReader(body))
	if err != nil {
		return Registration{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := s.HTTPClient.Do(req)
	if err != nil {
		return Registration{}, fmt.Errorf("dynamic client registration: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
		return Registration{}, fmt.Errorf("dynamic client registration returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	var registration Registration
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxMetadataBytes)).Decode(&registration); err != nil {
		return Registration{}, fmt.Errorf("decode dynamic registration: %w", err)
	}
	if registration.ClientID == "" {
		return Registration{}, fmt.Errorf("dynamic registration returned no client_id")
	}
	return registration, nil
}

func buildAuthorizationURL(endpoint, clientID, redirectURI, state, verifier string) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", err
	}
	query := u.Query()
	query.Set("response_type", "code")
	query.Set("client_id", clientID)
	query.Set("redirect_uri", redirectURI)
	query.Set("scope", config.Scope)
	query.Set("state", state)
	query.Set("code_challenge", pkce.GenerateChallenge(verifier))
	query.Set("code_challenge_method", "S256")
	query.Set("resource", config.Endpoint)
	u.RawQuery = query.Encode()
	return u.String(), nil
}

func (s *Service) exchangeCode(ctx context.Context, metadata AuthorizationServerMetadata, registration Registration, redirectURI, verifier, code string) (tokenResponse, error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"client_id":     {registration.ClientID},
		"code_verifier": {verifier},
		"resource":      {config.Endpoint},
	}
	return s.postToken(ctx, metadata.TokenEndpoint, form)
}

func (s *Service) postToken(ctx context.Context, endpoint string, form url.Values) (tokenResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return tokenResponse{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := s.HTTPClient.Do(req)
	if err != nil {
		return tokenResponse{}, err
	}
	defer resp.Body.Close()
	var token tokenResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxMetadataBytes)).Decode(&token); err != nil {
		return tokenResponse{}, fmt.Errorf("decode token response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || token.Error != "" {
		message := token.Error
		if token.Description != "" {
			message += ": " + token.Description
		}
		err := fmt.Errorf("token endpoint returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(message))
		// Only explicit grant/client rejections offer sign-in recovery. A 429 or
		// 5xx response must remain retryable even if its body names an old grant.
		if resp.StatusCode >= 200 && resp.StatusCode < 500 && resp.StatusCode != http.StatusTooManyRequests {
			switch token.Error {
			case "invalid_grant", "invalid_client", "unauthorized_client":
				return tokenResponse{}, &reauthenticationError{cause: err}
			}
		}
		return tokenResponse{}, err
	}
	return token, nil
}

func validateToken(discovery Discovery, registration Registration, token tokenResponse, now time.Time) (Credentials, error) {
	if token.AccessToken == "" {
		return Credentials{}, fmt.Errorf("token response did not include an access token")
	}
	if !strings.EqualFold(token.TokenType, "Bearer") {
		return Credentials{}, fmt.Errorf("token response used unsupported token type %q", token.TokenType)
	}
	if strings.TrimSpace(token.Scope) == "" {
		token.Scope = registration.Scope
	}
	if !scopeContains(token.Scope, config.Scope) {
		return Credentials{}, fmt.Errorf("token response did not grant required scope %q", config.Scope)
	}
	if token.ExpiresIn <= 0 {
		return Credentials{}, fmt.Errorf("token response did not include a valid expiry")
	}
	return Credentials{
		AccessToken:  token.AccessToken,
		RefreshToken: token.RefreshToken,
		TokenType:    "Bearer",
		Scope:        token.Scope,
		ExpiresAt:    now.UTC().Add(time.Duration(token.ExpiresIn) * time.Second),
		ClientID:     registration.ClientID,
		ClientSecret: registration.ClientSecret,
		Issuer:       normalizeURL(discovery.Authorization.Issuer),
		Resource:     normalizeURL(discovery.Resource.Resource),
	}, nil
}

func (s *Service) revoke(ctx context.Context, endpoint string, credentials Credentials) error {
	token := credentials.RefreshToken
	hint := "refresh_token"
	if token == "" {
		token = credentials.AccessToken
		hint = "access_token"
	}
	form := url.Values{
		"token":           {token},
		"client_id":       {credentials.ClientID},
		"token_type_hint": {hint},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := s.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}

func scopeContains(scope, required string) bool {
	for _, value := range strings.Fields(scope) {
		if value == required {
			return true
		}
	}
	return false
}

func openBrowser(target string) error {
	var command *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		command = exec.Command("open", target)
	case "windows":
		command = exec.Command("rundll32", "url.dll,FileProtocolHandler", target)
	default:
		command = exec.Command("xdg-open", target)
	}
	return command.Start()
}
