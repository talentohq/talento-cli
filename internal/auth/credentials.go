package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/basecamp/cli/credstore"
	baseprofile "github.com/basecamp/cli/profile"
	"github.com/talentohq/talento-cli/internal/config"
)

type Credentials struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	TokenType    string    `json:"token_type"`
	Scope        string    `json:"scope"`
	ExpiresAt    time.Time `json:"expires_at"`
	ClientID     string    `json:"client_id"`
	ClientSecret string    `json:"client_secret,omitempty"`
	Issuer       string    `json:"issuer"`
	Resource     string    `json:"resource"`
}

type CredentialStore interface {
	Load(profileName string) (Credentials, error)
	Save(profileName string, credentials Credentials) error
	Delete(profileName string) error
	UsingKeyring() bool
	Warning() string
}

type Store struct {
	store *credstore.Store
}

func NewCredentialStore(paths config.Paths, allowFileFallback bool) (*Store, error) {
	allowFallback := allowFileFallback || os.Getenv("TALENTO_ALLOW_FILE_CREDENTIALS") == "1"
	store := credstore.NewStore(credstore.StoreOptions{
		ServiceName:   "talento",
		DisableEnvVar: "TALENTO_NO_KEYRING",
		FallbackDir:   paths.CredentialDir,
	})
	if !store.UsingKeyring() && !allowFallback {
		return nil, fmt.Errorf("system credential store is unavailable; rerun with --allow-file-credentials or set TALENTO_ALLOW_FILE_CREDENTIALS=1 to opt in to an owner-only plaintext file")
	}
	return &Store{store: store}, nil
}

func (s *Store) Load(profileName string) (Credentials, error) {
	data, err := s.store.Load(baseprofile.CredentialKey(profileName, config.Endpoint))
	if err != nil {
		return Credentials{}, err
	}
	var credentials Credentials
	if err := json.Unmarshal(data, &credentials); err != nil {
		return Credentials{}, fmt.Errorf("decode stored credentials: %w", err)
	}
	return credentials, nil
}

func (s *Store) Save(profileName string, credentials Credentials) error {
	data, err := json.Marshal(credentials)
	if err != nil {
		return err
	}
	return s.store.Save(baseprofile.CredentialKey(profileName, config.Endpoint), data)
}

func (s *Store) Delete(profileName string) error {
	err := s.store.Delete(baseprofile.CredentialKey(profileName, config.Endpoint))
	if err != nil && !stringsContainMissing(err.Error()) {
		return err
	}
	return nil
}

func (s *Store) UsingKeyring() bool { return s.store.UsingKeyring() }
func (s *Store) Warning() string    { return s.store.FallbackWarning() }

func IsMissingCredentials(err error) bool {
	return err != nil && (errors.Is(err, os.ErrNotExist) || stringsContainMissing(err.Error()))
}

func stringsContainMissing(value string) bool {
	for _, marker := range []string{"not found", "credentials not found", "secret not found"} {
		if len(value) >= len(marker) && containsFold(value, marker) {
			return true
		}
	}
	return false
}

func containsFold(value, marker string) bool {
	return strings.Contains(strings.ToLower(value), strings.ToLower(marker))
}
