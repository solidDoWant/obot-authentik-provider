package profile

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/obot-platform/enterprise-providers/authcommon"
	"github.com/obot-platform/providers/auth-providers-common/pkg/state"
	"github.com/solidDoWant/obot-authentik-provider/pkg/client"
)

// FetchGroupPage reads one page of groups, sorted by name, which authentik keeps unique. The
// native cursor is authentik's page number; authcommon binds it to the filter and page size, so
// every page of a listing is read at the same size.
//
// A name filter is a case-insensitive substring match.
func FetchGroupPage(ctx context.Context, directory *client.Client, req authcommon.PageRequest) (authcommon.PageResult, error) {
	if req.Limit <= 0 || req.Limit > authcommon.MaxGroupPageSize {
		return authcommon.PageResult{}, errors.New("invalid page size")
	}
	page := 1
	if req.Cursor != "" {
		var err error
		if page, err = strconv.Atoi(req.Cursor); err != nil || page < 2 {
			return authcommon.PageResult{}, fmt.Errorf("%w: invalid group page", authcommon.ErrInvalidCursor)
		}
	}
	ctx, cancel := context.WithTimeout(ctx, membershipTimeout)
	defer cancel()

	query := groupDetailQuery()
	query.Set("ordering", "name")
	query.Set("page", strconv.Itoa(page))
	query.Set("page_size", strconv.Itoa(req.Limit))
	if req.NameFilter != "" {
		query.Set("search", req.NameFilter)
	}
	var response struct {
		Pagination *struct {
			Next    int `json:"next"`
			Current int `json:"current"`
		} `json:"pagination"`
		Results []group `json:"results"`
	}
	status, err := read(ctx, directory, "/core/groups/", query, &response)
	if err != nil {
		return authcommon.PageResult{}, err
	}
	if status == http.StatusNotFound && page > 1 {
		// Groups were deleted since the previous page, so this page no longer exists.
		return authcommon.PageResult{}, fmt.Errorf("%w: groups changed during paging", authcommon.ErrInvalidCursor)
	}
	if status != http.StatusOK {
		return authcommon.PageResult{}, fmt.Errorf("group listing returned HTTP %d", status)
	}
	if response.Pagination == nil || response.Pagination.Current != page || response.Results == nil || len(response.Results) > req.Limit {
		return authcommon.PageResult{}, errors.New("invalid group page")
	}

	items := make(state.GroupInfoList, 0, len(response.Results))
	seen := make(map[string]bool, len(response.Results))
	for _, g := range response.Results {
		if err := g.valid(); err != nil {
			return authcommon.PageResult{}, err
		}
		if seen[g.ID] {
			return authcommon.PageResult{}, errors.New("duplicate group in page")
		}
		seen[g.ID] = true
		items = append(items, g.info())
	}

	result := authcommon.PageResult{Items: items}
	if next := response.Pagination.Next; next != 0 {
		if next != page+1 {
			return authcommon.PageResult{}, errors.New("invalid group page")
		}
		result.NextCursor = strconv.Itoa(next)
	}
	return result, nil
}

// CheckDirectoryAccess reads one user and one group with the API token, so that a wrong token or
// missing permissions are reported before the first sign-in needs them.
func CheckDirectoryAccess(ctx context.Context, directory *client.Client) error {
	checks := []struct {
		path, permission string
		query            url.Values
	}{
		{"/core/users/", "Can view User", url.Values{"page_size": {"1"}, "include_groups": {"false"}, "include_roles": {"false"}}},
		{"/core/groups/", "Can view Group", url.Values{"page_size": {"1"}, "include_users": {"false"}}},
	}
	for _, check := range checks {
		var page struct {
			Results []map[string]any `json:"results"`
		}
		status, err := read(ctx, directory, check.path, check.query, &page)
		switch {
		case err != nil:
			return err
		case status == http.StatusUnauthorized:
			return errors.New("authentik refused the API token; check that it exists, has the API intent, and has not expired")
		case status == http.StatusForbidden:
			return fmt.Errorf("the API token's user lacks the %q permission", check.permission)
		case status == http.StatusNotFound:
			return fmt.Errorf("no authentik API at %s; check the Issuer URL", directory.APIURL())
		case status != http.StatusOK:
			return fmt.Errorf("directory read %s returned HTTP %d", check.path, status)
		case page.Results == nil:
			return fmt.Errorf("directory read %s returned an invalid response", check.path)
		}
	}
	return nil
}
