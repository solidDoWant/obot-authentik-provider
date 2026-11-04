// Package profile translates authentik identities and groups to Obot contracts.
package profile

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/obot-platform/enterprise-providers/authcommon"
	"github.com/obot-platform/providers/auth-providers-common/pkg/state"
	"github.com/solidDoWant/obot-authentik-provider/pkg/client"
)

const (
	// GroupIDPrefix is prepended to authentik group UUIDs on the Obot side.
	GroupIDPrefix = "authentik/"

	maxGroups              = 100000
	maxDepth               = 128
	directoryTimeout       = 30 * time.Second
	membershipTimeout      = 8 * time.Second
	groupLookupConcurrency = 8

	// SubjectModeError explains the one subject mode that lets Obot find users in authentik.
	SubjectModeError = "the subject is not an authentik user UUID; set the provider's Subject mode to \"Based on the User's UUID\""
)

var (
	// uuidPattern matches the lowercase hyphenated form that authentik uses for user and group UUIDs.
	uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
)

// UserUnavailableError deliberately maps to 403, never the unsupported-endpoint 404.
type UserUnavailableError struct{ Reason string }

type UserInfo struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	IconURL string `json:"icon_url,omitempty"`
}

type group struct {
	ID      string   `json:"pk"`
	Name    string   `json:"name"`
	Parents []string `json:"parents"`
}

func (e *UserUnavailableError) Error() string { return e.Reason }

// IsUUID reports whether id is a UUID in the form authentik uses.
func IsUUID(id string) bool { return uuidPattern.MatchString(id) }

func (g group) valid() error {
	if !IsUUID(g.ID) || g.Name == "" || g.Parents == nil {
		return errors.New("authentik group is missing its UUID, name, or parents")
	}
	return nil
}

func (g group) info() state.GroupInfo {
	return state.GroupInfo{ID: GroupIDPrefix + g.ID, Name: g.Name}
}

// GetUserInfo reads the user's profile with the user's own token. The subject must be the
// authentik user UUID, which is also the ID used to look up the user's groups.
func GetUserInfo(ctx context.Context, token, endpoint string) (*UserInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, directoryTimeout)
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
	if !IsUUID(user.Sub) {
		return nil, errors.New(SubjectModeError)
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

// FetchUserGroupInfos returns the user's direct groups and all of their ancestors, or an error.
// It never returns a partial answer.
func FetchUserGroupInfos(ctx context.Context, directory *client.Client, userID string) (state.GroupInfoList, error) {
	ctx, cancel := context.WithTimeout(ctx, membershipTimeout)
	defer cancel()
	if !IsUUID(userID) {
		return nil, errors.New("invalid authentik user UUID")
	}
	directIDs, err := fetchUserGroupIDs(ctx, directory, userID)
	if err != nil {
		return nil, err
	}

	// Walk up the parents breadth first. A group can have several parents, so each one is fetched
	// once no matter how many paths lead to it.
	memo := map[string]group{}
	scheduled := map[string]bool{}
	frontier := make([]string, 0, len(directIDs))
	for _, id := range directIDs {
		if !scheduled[id] {
			scheduled[id] = true
			frontier = append(frontier, id)
		}
	}
	for depth := 0; len(frontier) > 0; depth++ {
		if depth >= maxDepth {
			return nil, errors.New("authentik group ancestry exceeds safety bounds")
		}
		fetched, err := fetchGroups(ctx, directory, frontier)
		if err != nil {
			return nil, err
		}
		next := []string{}
		for _, g := range fetched {
			memo[g.ID] = g
			for _, parent := range g.Parents {
				if !IsUUID(parent) {
					return nil, errors.New("authentik group has an invalid parent UUID")
				}
				if scheduled[parent] {
					continue
				}
				if len(scheduled) >= maxGroups {
					return nil, errors.New("authentik group ancestry exceeds safety bounds")
				}
				scheduled[parent] = true
				next = append(next, parent)
			}
		}
		frontier = next
	}

	infos := make(state.GroupInfoList, 0, len(memo))
	for _, g := range memo {
		infos = append(infos, g.info())
	}
	sort.Slice(infos, func(i, j int) bool { return infos[i].ID < infos[j].ID })
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return infos, nil
}

// fetchUserGroupIDs returns the UUIDs of the user's direct groups, after checking that the user
// exists and is active.
func fetchUserGroupIDs(ctx context.Context, directory *client.Client, userID string) ([]string, error) {
	query := url.Values{"uuid": {userID}, "include_groups": {"false"}, "include_roles": {"false"}}
	var page struct {
		Results []struct {
			UUID     string   `json:"uuid"`
			IsActive *bool    `json:"is_active"`
			Groups   []string `json:"groups"`
		} `json:"results"`
	}
	status, err := read(ctx, directory, "/core/users/", query, &page)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("user lookup returned HTTP %d", status)
	}
	switch {
	case page.Results == nil || len(page.Results) > 1:
		return nil, errors.New("invalid user lookup response")
	case len(page.Results) == 0:
		return nil, &UserUnavailableError{Reason: "authentik user is unavailable"}
	}
	user := page.Results[0]
	if user.UUID != userID || user.IsActive == nil || user.Groups == nil {
		return nil, errors.New("invalid user lookup response")
	}
	if !*user.IsActive {
		return nil, &UserUnavailableError{Reason: "authentik user is deactivated"}
	}
	if len(user.Groups) > maxGroups {
		return nil, errors.New("oversized user memberships response")
	}
	for _, id := range user.Groups {
		if !IsUUID(id) {
			return nil, errors.New("user membership has an invalid group UUID")
		}
	}
	return user.Groups, nil
}

// fetchGroups reads every group in ids, failing if any read fails or any group is missing. A fixed
// worker count bounds both network concurrency and goroutine allocation.
func fetchGroups(ctx context.Context, directory *client.Client, ids []string) ([]group, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	groups := make([]group, len(ids))
	jobs := make(chan int)
	var wg sync.WaitGroup
	var failed sync.Once
	var firstErr error
	for range min(groupLookupConcurrency, len(ids)) {
		wg.Go(func() {
			for index := range jobs {
				if ctx.Err() != nil {
					return
				}
				g, found, err := fetchGroup(ctx, directory, ids[index])
				if err == nil && !found {
					// A group deleted while the user's memberships were read. Retrying gives a
					// consistent answer; silently dropping it could drop an ancestor too.
					err = errors.New("a membership group or ancestor was deleted during the lookup")
				}
				if err != nil {
					failed.Do(func() {
						firstErr = err
						cancel()
					})
					return
				}
				groups[index] = g
			}
		})
	}
	for index := 0; index < len(ids) && ctx.Err() == nil; index++ {
		select {
		case jobs <- index:
		case <-ctx.Done():
		}
	}
	close(jobs)
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return groups, nil
}

// fetchGroup reads one group. found is false when authentik has no group with that UUID.
func fetchGroup(ctx context.Context, directory *client.Client, id string) (group, bool, error) {
	if !IsUUID(id) {
		return group{}, false, nil
	}
	var g group
	status, err := read(ctx, directory, "/core/groups/"+id+"/", groupDetailQuery(), &g)
	if err != nil {
		return group{}, false, err
	}
	if status == http.StatusNotFound {
		return group{}, false, nil
	}
	if status != http.StatusOK {
		return group{}, false, fmt.Errorf("group lookup returned HTTP %d", status)
	}
	if err := g.valid(); err != nil {
		return group{}, false, err
	}
	if g.ID != id {
		return group{}, false, errors.New("group lookup returned a different UUID")
	}
	return g, true, nil
}

// groupDetailQuery leaves out the members, which can be large, and the other relations Obot
// doesn't use.
func groupDetailQuery() url.Values {
	return url.Values{
		"include_users":           {"false"},
		"include_children":        {"false"},
		"include_parents":         {"false"},
		"include_inherited_roles": {"false"},
	}
}

// FetchGroupsByIDs omits invalid or missing IDs but fails on any permission, transport, or parse
// error.
func FetchGroupsByIDs(ctx context.Context, directory *client.Client, ids []string) (state.GroupInfoList, error) {
	ctx, cancel := context.WithTimeout(ctx, directoryTimeout)
	defer cancel()
	ctx, cancelWork := context.WithCancel(ctx)
	defer cancelWork()
	if len(ids) > authcommon.MaxGroupIDsPerRequest {
		return nil, authcommon.ErrTooManyIDs
	}
	infos := make(state.GroupInfoList, 0, len(ids))
	var mu sync.Mutex
	var wg sync.WaitGroup
	var firstErr error
	sem := make(chan struct{}, groupLookupConcurrency)
	seen := map[string]bool{}
	for index := 0; index < len(ids) && ctx.Err() == nil; index++ {
		id := ids[index]
		if !IsUUID(id) || seen[id] {
			continue
		}
		seen[id] = true
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			continue
		}
		wg.Go(func() {
			defer func() { <-sem }()
			g, found, err := fetchGroup(ctx, directory, id)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if firstErr == nil {
					firstErr = err
					cancelWork()
				}
				return
			}
			if found {
				infos = append(infos, g.info())
			}
		})
	}
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	sort.Slice(infos, func(i, j int) bool { return infos[i].ID < infos[j].ID })
	return infos, nil
}

func read(ctx context.Context, directory *client.Client, path string, query url.Values, out any) (int, error) {
	response, err := directory.Get(ctx, path, query)
	if err != nil {
		return 0, fmt.Errorf("authentik API read: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return response.StatusCode, nil
	}
	if err := client.Decode(response.Body, out); err != nil {
		return 0, fmt.Errorf("decode authentik API response: %w", err)
	}
	return response.StatusCode, nil
}
