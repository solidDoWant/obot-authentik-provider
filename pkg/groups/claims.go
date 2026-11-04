// Package groups reads users' authentik groups from their ID tokens and keeps them, so that Obot
// can look groups up between sign-ins.
package groups

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/obot-platform/providers/auth-providers-common/pkg/state"
	"github.com/solidDoWant/obot-authentik-provider/pkg/profile"
)

const (
	// Claim is the ID token claim, and Scope the OAuth scope that requests it, holding the user's
	// groups and all of their ancestors.
	Claim = "obot_groups"
	Scope = "obot_groups"

	// IDPrefix is prepended to authentik group UUIDs on the Obot side.
	IDPrefix = "authentik/"

	maxGroups = 1000
)

var (
	// ErrNoClaim means the ID token has no groups claim, which happens when the provider has no
	// scope mapping for Scope.
	ErrNoClaim = errors.New("the ID token has no " + Claim + " claim; add the " + Scope + " scope mapping to the authentik provider")
)

// Group is an authentik group, by its UUID and name.
type Group struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Info returns the group as Obot sees it.
func (g Group) Info() state.GroupInfo {
	return state.GroupInfo{ID: IDPrefix + g.ID, Name: g.Name}
}

// FromIDToken reads the groups claim of an ID token that oauth2-proxy has already verified. It
// returns ErrNoClaim when the claim is absent, and another error when it's malformed.
func FromIDToken(idToken string) ([]Group, error) {
	parts := strings.Split(idToken, ".")
	if len(parts) != 3 {
		return nil, errors.New("the ID token is not a JWT")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("decode the ID token: %w", err)
	}
	var claims map[string]json.RawMessage
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, fmt.Errorf("decode the ID token: %w", err)
	}
	raw, ok := claims[Claim]
	if !ok {
		return nil, ErrNoClaim
	}

	var groups []Group
	if err := json.Unmarshal(raw, &groups); err != nil || groups == nil {
		return nil, fmt.Errorf("the %s claim must be a list of {\"id\": <group UUID>, \"name\": <group name>}", Claim)
	}
	if len(groups) > maxGroups {
		return nil, fmt.Errorf("the %s claim has %d groups, more than %d", Claim, len(groups), maxGroups)
	}
	seen := make(map[string]bool, len(groups))
	unique := make([]Group, 0, len(groups))
	for _, g := range groups {
		if !profile.IsUUID(g.ID) || strings.TrimSpace(g.Name) == "" {
			return nil, fmt.Errorf("the %s claim has a group without a UUID or name", Claim)
		}
		if !seen[g.ID] {
			seen[g.ID] = true
			unique = append(unique, g)
		}
	}
	return unique, nil
}
