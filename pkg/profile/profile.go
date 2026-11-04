// Package profile translates authentik user profiles to Obot contracts.
package profile

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/solidDoWant/obot-authentik-provider/pkg/client"
)

const (
	userInfoTimeout = 30 * time.Second
)

var (
	// uuidPattern matches the lowercase hyphenated form that authentik uses for UUIDs.
	uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
)

type UserInfo struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	IconURL string `json:"icon_url,omitempty"`
}

// IsUUID reports whether id is a UUID in the form authentik uses.
func IsUUID(id string) bool { return uuidPattern.MatchString(id) }

// GetUserInfo reads the user's profile with the user's own token.
func GetUserInfo(ctx context.Context, token, endpoint string) (*UserInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, userInfoTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	response, err := (&http.Client{Timeout: 15 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch userinfo: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("userinfo returned HTTP %d", response.StatusCode)
	}
	var user struct {
		Sub      string `json:"sub"`
		Email    string `json:"email"`
		Name     string `json:"name"`
		Username string `json:"preferred_username"`
		Picture  string `json:"picture"`
	}
	if err := client.Decode(response.Body, &user); err != nil {
		return nil, err
	}
	if user.Sub == "" {
		return nil, errors.New("userinfo has no subject")
	}
	name := firstNonempty(user.Name, user.Username, user.Email)
	picture, err := url.Parse(user.Picture)
	if err != nil || picture.Scheme != "https" || picture.Host == "" || picture.User != nil {
		user.Picture = ""
	}
	return &UserInfo{ID: user.Sub, Name: name, IconURL: user.Picture}, nil
}

func firstNonempty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
