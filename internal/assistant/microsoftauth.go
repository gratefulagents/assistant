// SPDX-License-Identifier: GPL-3.0-only

package assistant

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// microsoftScopeAliases maps short, user-friendly scope names to their
// canonical Microsoft Graph scope URLs so flags and env vars can use either
// form.
var microsoftScopeAliases = map[string]string{
	"mail.read":           "https://graph.microsoft.com/Mail.Read",
	"mail.send":           "https://graph.microsoft.com/Mail.Send",
	"calendars.read":      "https://graph.microsoft.com/Calendars.Read",
	"calendars.readwrite": "https://graph.microsoft.com/Calendars.ReadWrite",
	"contacts.read":       "https://graph.microsoft.com/Contacts.Read",
	"files.read":          "https://graph.microsoft.com/Files.Read",
}

func defaultMicrosoftScopes() []string {
	return []string{"https://graph.microsoft.com/Mail.Read"}
}

// normalizeMicrosoftScopes expands aliases to canonical URLs and de-duplicates.
func normalizeMicrosoftScopes(scopes []string) []string {
	out := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		scope = strings.TrimSpace(scope)
		if scope == "" {
			continue
		}
		if full, ok := microsoftScopeAliases[strings.ToLower(scope)]; ok {
			scope = full
		}
		out = append(out, scope)
	}
	return uniqueStrings(out)
}

// hasMicrosoftScope reports whether any granted scope matches name (e.g.
// "Mail.Read"). Microsoft may return scopes in short form ("Mail.Read") or as
// full Graph URLs, so match the suffix case-insensitively.
func hasMicrosoftScope(scopes []string, name string) bool {
	name = strings.ToLower(name)
	for _, scope := range scopes {
		if strings.Contains(strings.ToLower(scope), name) {
			return true
		}
	}
	return false
}

// microsoftAuthFile is the local credential the assistant stores after pairing
// with the Connect broker for a Microsoft account. Like googleAuthFile it holds
// a long-lived pairing secret (not a Microsoft token) plus the most recently
// minted short-lived Graph access token.
type microsoftAuthFile struct {
	BrokerURL   string    `json:"broker_url"`
	AssistantID string    `json:"assistant_id"`
	Secret      string    `json:"secret"`
	Scopes      []string  `json:"scopes,omitempty"`
	Email       string    `json:"email,omitempty"`
	AccessToken string    `json:"access_token,omitempty"`
	Expiry      time.Time `json:"expiry,omitempty"`
}

func microsoftAuthPath(cfg appConfig) string {
	if path := strings.TrimSpace(cfg.MicrosoftAuthPath); path != "" {
		return path
	}
	return stateFilePath(cfg, "microsoft-auth.json")
}

func loadMicrosoftAuthFile(path string) (microsoftAuthFile, bool, error) {
	var file microsoftAuthFile
	exists, err := readJSONFile(path, &file)
	return file, exists, err
}

func saveMicrosoftAuthFile(path string, file microsoftAuthFile) error {
	return writeJSONFile(path, file)
}

func microsoftAuthConfigured(cfg appConfig) bool {
	_, exists, err := loadMicrosoftAuthFile(microsoftAuthPath(cfg))
	return exists && err == nil
}

func microsoftConnectedScopes(cfg appConfig) []string {
	file, exists, err := loadMicrosoftAuthFile(microsoftAuthPath(cfg))
	if !exists || err != nil {
		return nil
	}
	return file.Scopes
}

// errMicrosoftReconnect signals that the stored grant is no longer valid and
// the user must run microsoft-connect again.
var errMicrosoftReconnect = errors.New("microsoft grant is no longer valid; run `assistant microsoft-connect`")

// microsoftAuthSession mints short-lived Microsoft Graph access tokens from the
// broker using the stored pairing credential, caching the access token until
// near expiry. It speaks the same broker /token protocol as googleAuthSession.
type microsoftAuthSession struct {
	path   string
	client *http.Client
	now    func() time.Time

	mu   sync.Mutex
	file microsoftAuthFile
}

func newMicrosoftAuthSession(cfg appConfig) (*microsoftAuthSession, error) {
	path := microsoftAuthPath(cfg)
	file, exists, err := loadMicrosoftAuthFile(path)
	if err != nil {
		return nil, fmt.Errorf("load Microsoft auth file %s: %w", path, err)
	}
	if !exists {
		return nil, fmt.Errorf("no Microsoft credential at %s; run `assistant microsoft-connect`", path)
	}
	if strings.TrimSpace(file.BrokerURL) == "" || strings.TrimSpace(file.AssistantID) == "" || strings.TrimSpace(file.Secret) == "" {
		return nil, fmt.Errorf("Microsoft auth file %s is incomplete; run `assistant microsoft-connect`", path)
	}
	return &microsoftAuthSession{path: path, client: defaultHTTPClient, now: time.Now, file: file}, nil
}

// AccessToken returns a cached Microsoft access token when it is still valid,
// or mints a fresh one from the broker.
func (s *microsoftAuthSession) AccessToken(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.TrimSpace(s.file.AccessToken) != "" && s.now().Add(60*time.Second).Before(s.file.Expiry) {
		return s.file.AccessToken, nil
	}
	return s.refreshLocked(ctx)
}

func (s *microsoftAuthSession) refreshLocked(ctx context.Context) (string, error) {
	resp, err := brokerPostJSON[brokerTokenResponse](ctx, s.client, strings.TrimRight(s.file.BrokerURL, "/")+"/token", map[string]string{
		"provider":     "microsoft",
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
			_ = saveMicrosoftAuthFile(s.path, s.file)
			return "", errMicrosoftReconnect
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
	if err := saveMicrosoftAuthFile(s.path, s.file); err != nil {
		return "", err
	}
	return s.file.AccessToken, nil
}
