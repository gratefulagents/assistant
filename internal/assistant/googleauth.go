// SPDX-License-Identifier: GPL-3.0-only

package assistant

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// googleScopeAliases maps short, user-friendly scope names to their canonical
// Google OAuth scope URLs so flags and env vars can use either form.
var googleScopeAliases = map[string]string{
	"gmail.readonly":    "https://www.googleapis.com/auth/gmail.readonly",
	"gmail.modify":      "https://www.googleapis.com/auth/gmail.modify",
	"gmail.send":        "https://www.googleapis.com/auth/gmail.send",
	"calendar.readonly": "https://www.googleapis.com/auth/calendar.readonly",
	"calendar":          "https://www.googleapis.com/auth/calendar",
	"drive.readonly":    "https://www.googleapis.com/auth/drive.readonly",
	"contacts.readonly": "https://www.googleapis.com/auth/contacts.readonly",
}

func defaultGoogleScopes() []string {
	return []string{"https://www.googleapis.com/auth/gmail.readonly"}
}

// normalizeGoogleScopes expands aliases to canonical URLs and de-duplicates.
func normalizeGoogleScopes(scopes []string) []string {
	out := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		scope = strings.TrimSpace(scope)
		if scope == "" {
			continue
		}
		if full, ok := googleScopeAliases[scope]; ok {
			scope = full
		}
		out = append(out, scope)
	}
	return uniqueStrings(out)
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// randomHex returns n random bytes encoded as a hex string. It is used to mint
// the client-side pairing identifiers and secrets sent to the Connect broker.
func randomHex(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return hex.EncodeToString([]byte(time.Now().Format(time.RFC3339Nano)))
	}
	return hex.EncodeToString(buf)
}

// googleAuthFile is the local credential the assistant stores after pairing with
// the Connect broker. It holds a long-lived pairing secret (not a Google token)
// plus the most recently minted short-lived Google access token.
type googleAuthFile struct {
	BrokerURL   string    `json:"broker_url"`
	AssistantID string    `json:"assistant_id"`
	Secret      string    `json:"secret"`
	Scopes      []string  `json:"scopes,omitempty"`
	Email       string    `json:"email,omitempty"`
	AccessToken string    `json:"access_token,omitempty"`
	Expiry      time.Time `json:"expiry,omitempty"`
}

func googleAuthPath(cfg appConfig) string {
	if path := strings.TrimSpace(cfg.GoogleAuthPath); path != "" {
		return path
	}
	return stateFilePath(cfg, "google-auth.json")
}

func loadGoogleAuthFile(path string) (googleAuthFile, bool, error) {
	var file googleAuthFile
	exists, err := readJSONFile(path, &file)
	return file, exists, err
}

func saveGoogleAuthFile(path string, file googleAuthFile) error {
	return writeJSONFile(path, file)
}

func googleAuthConfigured(cfg appConfig) bool {
	_, exists, err := loadGoogleAuthFile(googleAuthPath(cfg))
	return exists && err == nil
}

// brokerTokenResponse is returned by the broker's /token endpoint.
type brokerTokenResponse struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"`
	Scopes      string `json:"scopes"`
	Email       string `json:"email"`
	Error       string `json:"error"`
}

// errGoogleReconnect signals that the stored grant is no longer valid and the
// user must run google-connect again.
var errGoogleReconnect = errors.New("google grant is no longer valid; run `assistant google-connect`")

// googleAuthSession mints short-lived Google access tokens from the broker using
// the stored pairing credential, caching the access token until near expiry.
type googleAuthSession struct {
	path   string
	client *http.Client
	now    func() time.Time

	mu   sync.Mutex
	file googleAuthFile
}

func newGoogleAuthSession(cfg appConfig) (*googleAuthSession, error) {
	path := googleAuthPath(cfg)
	file, exists, err := loadGoogleAuthFile(path)
	if err != nil {
		return nil, fmt.Errorf("load Google auth file %s: %w", path, err)
	}
	if !exists {
		return nil, fmt.Errorf("no Google credential at %s; run `assistant google-connect`", path)
	}
	if strings.TrimSpace(file.BrokerURL) == "" || strings.TrimSpace(file.AssistantID) == "" || strings.TrimSpace(file.Secret) == "" {
		return nil, fmt.Errorf("Google auth file %s is incomplete; run `assistant google-connect`", path)
	}
	return &googleAuthSession{path: path, client: defaultHTTPClient, now: time.Now, file: file}, nil
}

// AccessToken returns a cached Google access token when it is still valid, or
// mints a fresh one from the broker.
func (s *googleAuthSession) AccessToken(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.TrimSpace(s.file.AccessToken) != "" && s.now().Add(60*time.Second).Before(s.file.Expiry) {
		return s.file.AccessToken, nil
	}
	return s.refreshLocked(ctx)
}

func (s *googleAuthSession) refreshLocked(ctx context.Context) (string, error) {
	resp, err := brokerPostJSON[brokerTokenResponse](ctx, s.client, strings.TrimRight(s.file.BrokerURL, "/")+"/token", map[string]string{
		"assistant_id": s.file.AssistantID,
		"secret":       s.file.Secret,
	})
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(resp.Error) != "" {
		if resp.Error == "invalid_grant" || resp.Error == "invalid_secret" {
			s.file.AccessToken = ""
			s.file.Expiry = time.Time{}
			_ = saveGoogleAuthFile(s.path, s.file)
			return "", errGoogleReconnect
		}
		return "", fmt.Errorf("broker token error: %s", resp.Error)
	}
	if strings.TrimSpace(resp.AccessToken) == "" {
		return "", errors.New("broker returned an empty access token")
	}
	s.file.AccessToken = resp.AccessToken
	if resp.ExpiresIn > 0 {
		s.file.Expiry = s.now().Add(time.Duration(resp.ExpiresIn) * time.Second)
	} else {
		s.file.Expiry = s.now().Add(time.Hour)
	}
	if strings.TrimSpace(resp.Email) != "" {
		s.file.Email = resp.Email
	}
	if strings.TrimSpace(resp.Scopes) != "" {
		s.file.Scopes = strings.Fields(resp.Scopes)
	}
	if err := saveGoogleAuthFile(s.path, s.file); err != nil {
		return "", err
	}
	return s.file.AccessToken, nil
}

// brokerPostJSON posts payload as JSON and decodes the response body into T. A
// non-2xx status is decoded too so callers can read structured error fields.
func brokerPostJSON[T any](ctx context.Context, client *http.Client, endpoint string, payload any) (T, error) {
	var out T
	body, err := json.Marshal(payload)
	if err != nil {
		return out, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(body)))
	if err != nil {
		return out, err
	}
	req.Header.Set("Content-Type", "application/json")
	if client == nil {
		client = defaultHTTPClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return out, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return out, err
	}
	if len(data) == 0 {
		if resp.StatusCode >= 300 {
			return out, fmt.Errorf("POST %s: %s", endpoint, resp.Status)
		}
		return out, nil
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return out, fmt.Errorf("POST %s: %s: %s", endpoint, resp.Status, firstLine(string(data)))
	}
	return out, nil
}
