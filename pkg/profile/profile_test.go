package profile

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/obot-platform/enterprise-providers/authcommon"
	"github.com/obot-platform/providers/auth-providers-common/pkg/state"
	"github.com/solidDoWant/obot-authentik-provider/pkg/client"
)

const (
	userID   = "11111111-1111-4111-8111-111111111111"
	engID    = "22222222-2222-4222-8222-222222222222"
	opsID    = "33333333-3333-4333-8333-333333333333"
	techID   = "44444444-4444-4444-8444-444444444444"
	staffID  = "55555555-5555-4555-8555-555555555555"
	otherID  = "66666666-6666-4666-8666-666666666666"
	absentID = "77777777-7777-4777-8777-777777777777"
)

type fakeUser struct {
	UUID     string   `json:"uuid"`
	IsActive bool     `json:"is_active"`
	Groups   []string `json:"groups"`
}

// fakeAuthentik serves the parts of authentik's API that the provider reads.
type fakeAuthentik struct {
	t      *testing.T
	token  string
	users  []fakeUser
	groups map[string]group
	// status, when set, is returned for every request.
	status int

	mu       sync.Mutex
	requests []string
}

func newFakeAuthentik(t *testing.T) *fakeAuthentik {
	t.Helper()
	// eng and ops are both children of tech, which is a child of staff: a diamond.
	return &fakeAuthentik{
		t:     t,
		token: "api-token",
		users: []fakeUser{{UUID: userID, IsActive: true, Groups: []string{engID, opsID}}},
		groups: map[string]group{
			engID:   {ID: engID, Name: "engineering", Parents: []string{techID}},
			opsID:   {ID: opsID, Name: "operations", Parents: []string{techID}},
			techID:  {ID: techID, Name: "technology", Parents: []string{staffID}},
			staffID: {ID: staffID, Name: "staff", Parents: []string{}},
			otherID: {ID: otherID, Name: "other", Parents: []string{}},
		},
	}
}

func (f *fakeAuthentik) start() *client.Client {
	server := httptest.NewServer(f)
	f.t.Cleanup(server.Close)
	return client.New(server.URL+"/api/v3", f.token, server.Client())
}

func (f *fakeAuthentik) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.requests = append(f.requests, r.URL.Path)
	f.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer "+f.token {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if f.status != 0 {
		w.WriteHeader(f.status)
		return
	}
	query := r.URL.Query()
	path := strings.TrimPrefix(r.URL.Path, "/api/v3")
	switch {
	case path == "/core/users/":
		results := []fakeUser{}
		for _, u := range f.users {
			if query.Get("uuid") == "" || query.Get("uuid") == u.UUID {
				results = append(results, u)
			}
		}
		f.write(w, map[string]any{"pagination": map[string]any{"current": 1, "next": 0}, "results": results})
	case path == "/core/groups/":
		if query.Get("include_users") != "false" {
			f.t.Error("group listing includes members")
		}
		f.listGroups(w, query.Get("search"), query.Get("page"), query.Get("page_size"))
	case strings.HasPrefix(path, "/core/groups/"):
		if query.Get("include_users") != "false" {
			f.t.Error("group read includes members")
		}
		g, ok := f.groups[strings.TrimSuffix(strings.TrimPrefix(path, "/core/groups/"), "/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		f.write(w, g)
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeAuthentik) listGroups(w http.ResponseWriter, search, pageParam, sizeParam string) {
	all := []group{}
	for _, g := range f.groups {
		if strings.Contains(strings.ToLower(g.Name), strings.ToLower(search)) {
			all = append(all, g)
		}
	}
	slices.SortFunc(all, func(a, b group) int { return strings.Compare(a.Name, b.Name) })
	page, size := 1, 20
	if pageParam != "" {
		page, _ = strconv.Atoi(pageParam)
	}
	if sizeParam != "" {
		size, _ = strconv.Atoi(sizeParam)
	}
	start := (page - 1) * size
	if start > len(all) || (start == len(all) && page > 1) {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	end := min(start+size, len(all))
	next := 0
	if end < len(all) {
		next = page + 1
	}
	f.write(w, map[string]any{"pagination": map[string]any{"current": page, "next": next}, "results": all[start:end]})
}

func (f *fakeAuthentik) write(w http.ResponseWriter, body any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(body); err != nil {
		f.t.Error(err)
	}
}

func ids(infos state.GroupInfoList) []string {
	out := make([]string, 0, len(infos))
	for _, info := range infos {
		out = append(out, info.ID)
	}
	return out
}

func TestFetchUserGroupInfosIncludesEveryAncestorOnce(t *testing.T) {
	fake := newFakeAuthentik(t)
	infos, err := FetchUserGroupInfos(t.Context(), fake.start(), userID)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"authentik/" + engID, "authentik/" + opsID, "authentik/" + techID, "authentik/" + staffID}
	slices.Sort(want)
	if got := ids(infos); !slices.Equal(got, want) {
		t.Fatalf("groups %v, want %v", got, want)
	}
	for _, info := range infos {
		if info.Name == "" {
			t.Fatalf("group %s has no name", info.ID)
		}
	}
	reads := map[string]int{}
	for _, path := range fake.requests {
		reads[path]++
	}
	if reads["/api/v3/core/groups/"+techID+"/"] != 1 {
		t.Fatalf("shared ancestor read %d times", reads["/api/v3/core/groups/"+techID+"/"])
	}
}

func TestFetchUserGroupInfosWithoutGroups(t *testing.T) {
	fake := newFakeAuthentik(t)
	fake.users[0].Groups = []string{}
	infos, err := FetchUserGroupInfos(t.Context(), fake.start(), userID)
	if err != nil || infos == nil || len(infos) != 0 {
		t.Fatalf("groups %v, error %v", infos, err)
	}
}

func TestFetchUserGroupInfosToleratesParentCycles(t *testing.T) {
	fake := newFakeAuthentik(t)
	fake.groups[staffID] = group{ID: staffID, Name: "staff", Parents: []string{engID}}
	infos, err := FetchUserGroupInfos(t.Context(), fake.start(), userID)
	if err != nil || len(infos) != 4 {
		t.Fatalf("groups %v, error %v", ids(infos), err)
	}
}

func TestFetchUserGroupInfosUnavailableUser(t *testing.T) {
	for name, mutate := range map[string]func(*fakeAuthentik){
		"missing":     func(f *fakeAuthentik) { f.users = nil },
		"deactivated": func(f *fakeAuthentik) { f.users[0].IsActive = false },
	} {
		t.Run(name, func(t *testing.T) {
			fake := newFakeAuthentik(t)
			mutate(fake)
			_, err := FetchUserGroupInfos(t.Context(), fake.start(), userID)
			if _, ok := errors.AsType[*UserUnavailableError](err); !ok {
				t.Fatalf("error %v is not UserUnavailableError", err)
			}
		})
	}
}

func TestFetchUserGroupInfosFailsWithoutPartialAnswers(t *testing.T) {
	for name, mutate := range map[string]func(*fakeAuthentik){
		"missing ancestor": func(f *fakeAuthentik) { delete(f.groups, staffID) },
		"invalid parent": func(f *fakeAuthentik) {
			f.groups[engID] = group{ID: engID, Name: "engineering", Parents: []string{"not-a-uuid"}}
		},
		"wrong group":        func(f *fakeAuthentik) { f.groups[engID] = f.groups[opsID] },
		"group without name": func(f *fakeAuthentik) { f.groups[engID] = group{ID: engID, Parents: []string{}} },
		"forbidden":          func(f *fakeAuthentik) { f.status = http.StatusForbidden },
		"bad token":          func(f *fakeAuthentik) { f.token = "other" },
	} {
		t.Run(name, func(t *testing.T) {
			fake := newFakeAuthentik(t)
			directory := fake.start()
			mutate(fake)
			infos, err := FetchUserGroupInfos(t.Context(), directory, userID)
			if err == nil {
				t.Fatalf("returned %v", ids(infos))
			}
			if _, ok := errors.AsType[*UserUnavailableError](err); ok {
				t.Fatal("a directory failure was reported as an unavailable user")
			}
		})
	}
}

func TestFetchUserGroupInfosRejectsNonUUID(t *testing.T) {
	fake := newFakeAuthentik(t)
	if _, err := FetchUserGroupInfos(t.Context(), fake.start(), "../core/users"); err == nil {
		t.Fatal("accepted a non-UUID user ID")
	}
	if len(fake.requests) != 0 {
		t.Fatal("sent a request for a non-UUID user ID")
	}
}

func TestFetchGroupsByIDs(t *testing.T) {
	fake := newFakeAuthentik(t)
	directory := fake.start()
	infos, err := FetchGroupsByIDs(t.Context(), directory, []string{engID, absentID, "not-a-uuid", engID, staffID})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"authentik/" + engID, "authentik/" + staffID}
	slices.Sort(want)
	if got := ids(infos); !slices.Equal(got, want) {
		t.Fatalf("groups %v, want %v", got, want)
	}

	fake.status = http.StatusForbidden
	if _, err := FetchGroupsByIDs(t.Context(), directory, []string{engID}); err == nil {
		t.Fatal("a permission error was treated as a missing group")
	}
}

func TestFetchGroupPage(t *testing.T) {
	fake := newFakeAuthentik(t)
	directory := fake.start()
	var names []string
	cursor := ""
	for range 10 {
		page, err := FetchGroupPage(t.Context(), directory, authcommon.PageRequest{Limit: 2, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range page.Items {
			names = append(names, item.Name)
		}
		if cursor = page.NextCursor; cursor == "" {
			break
		}
	}
	if want := []string{"engineering", "operations", "other", "staff", "technology"}; !slices.Equal(names, want) {
		t.Fatalf("names %v, want %v", names, want)
	}

	page, err := FetchGroupPage(t.Context(), directory, authcommon.PageRequest{Limit: 10, NameFilter: "OP"})
	if err != nil || len(page.Items) != 1 || page.Items[0].Name != "operations" || page.NextCursor != "" {
		t.Fatalf("filtered page %v, error %v", page, err)
	}
}

func TestFetchGroupPageCursors(t *testing.T) {
	fake := newFakeAuthentik(t)
	directory := fake.start()
	for _, cursor := range []string{"0", "1", "-1", "x"} {
		if _, err := FetchGroupPage(t.Context(), directory, authcommon.PageRequest{Limit: 2, Cursor: cursor}); !errors.Is(err, authcommon.ErrInvalidCursor) {
			t.Fatalf("cursor %q: error %v", cursor, err)
		}
	}
	// A page past the end, after groups were deleted, means restart from the top.
	if _, err := FetchGroupPage(t.Context(), directory, authcommon.PageRequest{Limit: 2, Cursor: "9"}); !errors.Is(err, authcommon.ErrInvalidCursor) {
		t.Fatalf("stale page: error %v", err)
	}
}

func TestCheckDirectoryAccess(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		token  string
		want   string
	}{
		{name: "ok"},
		{name: "bad token", token: "other", want: "refused the API token"},
		{name: "no permission", status: http.StatusForbidden, want: "Can view User"},
		{name: "no API", status: http.StatusNotFound, want: "no authentik API"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := newFakeAuthentik(t)
			directory := fake.start()
			fake.status = tc.status
			if tc.token != "" {
				fake.token = tc.token
			}
			err := CheckDirectoryAccess(t.Context(), directory)
			if tc.want == "" && err != nil || tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
				t.Fatalf("error %v, want %q", err, tc.want)
			}
		})
	}
}

func TestGetUserInfo(t *testing.T) {
	for _, tc := range []struct {
		name     string
		userinfo map[string]any
		want     *UserInfo
		wantErr  string
	}{
		{
			name:     "full profile",
			userinfo: map[string]any{"sub": userID, "name": "Person", "preferred_username": "person", "picture": "https://img.example/a.png"},
			want:     &UserInfo{ID: userID, Name: "Person", IconURL: "https://img.example/a.png"},
		},
		{
			name:     "username and unsafe picture",
			userinfo: map[string]any{"sub": userID, "preferred_username": "person", "picture": "http://img.example/a.png"},
			want:     &UserInfo{ID: userID, Name: "person"},
		},
		{
			name:     "hashed subject",
			userinfo: map[string]any{"sub": strings.Repeat("a", 64), "name": "Person"},
			wantErr:  "Subject mode",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer user-token" {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				if err := json.NewEncoder(w).Encode(tc.userinfo); err != nil {
					t.Error(err)
				}
			}))
			defer server.Close()
			got, err := GetUserInfo(t.Context(), "user-token", server.URL)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil || *got != *tc.want {
				t.Fatalf("user %+v, error %v, want %+v", got, err, tc.want)
			}
		})
	}
}
