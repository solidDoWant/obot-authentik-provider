package groups

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/obot-platform/enterprise-providers/authcommon"
	"github.com/obot-platform/providers/auth-providers-common/pkg/state"
)

const (
	alice = "alice-subject"
	bob   = "bob-subject"
	eng   = "22222222-2222-4222-8222-222222222222"
	ops   = "33333333-3333-4333-8333-333333333333"
	tech  = "44444444-4444-4444-8444-444444444444"
	other = "55555555-5555-4555-8555-555555555555"
)

type countingStore struct {
	Store
	writes int
}

func idToken(payload string) string {
	return "header." + base64.RawURLEncoding.EncodeToString([]byte(payload)) + ".signature"
}

func TestFromIDToken(t *testing.T) {
	got, err := FromIDToken(idToken(`{"obot_groups": [{"id": "` + eng + `", "name": "engineering"}, {"id": "` + eng + `", "name": "engineering"}, {"id": "` + tech + `", "name": "technology"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if want := []Group{{ID: eng, Name: "engineering"}, {ID: tech, Name: "technology"}}; !slices.Equal(got, want) {
		t.Fatalf("groups %v, want %v", got, want)
	}

	if got, err := FromIDToken(idToken(`{"obot_groups": []}`)); err != nil || got == nil || len(got) != 0 {
		t.Fatalf("empty claim: groups %v, error %v", got, err)
	}
	if _, err := FromIDToken(idToken(`{"groups": ["engineering"]}`)); !errors.Is(err, ErrNoClaim) {
		t.Fatalf("missing claim: error %v", err)
	}
	for name, payload := range map[string]string{
		"names only":   `{"obot_groups": ["engineering"]}`,
		"not a list":   `{"obot_groups": "engineering"}`,
		"null":         `{"obot_groups": null}`,
		"bad UUID":     `{"obot_groups": [{"id": "engineering", "name": "engineering"}]}`,
		"no name":      `{"obot_groups": [{"id": "` + eng + `", "name": " "}]}`,
		"not JSON":     `{`,
		"too many ids": `{"obot_groups": [` + strings.Repeat(`{"id": "`+eng+`", "name": "x"},`, maxGroups) + `{"id": "` + eng + `", "name": "x"}]}`,
	} {
		if _, err := FromIDToken(idToken(payload)); err == nil || errors.Is(err, ErrNoClaim) {
			t.Errorf("%s: error %v", name, err)
		}
	}
	if _, err := FromIDToken("opaque"); err == nil {
		t.Error("accepted a token that isn't a JWT")
	}
}

// stores runs a test against every Store. Postgres runs when OBOT_AUTHENTIK_TEST_POSTGRES_DSN names a
// database the test may write to.
func stores(t *testing.T, test func(t *testing.T, store Store)) {
	t.Helper()
	t.Run("memory", func(t *testing.T) { test(t, NewMemory()) })
	t.Run("postgres", func(t *testing.T) {
		dsn := os.Getenv("OBOT_AUTHENTIK_TEST_POSTGRES_DSN")
		if dsn == "" {
			t.Skip("OBOT_AUTHENTIK_TEST_POSTGRES_DSN is not set")
		}
		store, err := NewPostgres(t.Context(), dsn, 4)
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		if _, err := store.pool.Exec(t.Context(), "TRUNCATE "+table); err != nil {
			t.Fatal(err)
		}
		test(t, store)
	})
}

func ids(infos state.GroupInfoList) []string {
	out := make([]string, 0, len(infos))
	for _, info := range infos {
		out = append(out, strings.TrimPrefix(info.ID, IDPrefix)+"="+info.Name)
	}
	slices.Sort(out)
	return out
}

func set(t *testing.T, store Store, user string, at time.Time, groups ...Group) {
	t.Helper()
	if err := store.SetUserGroups(context.Background(), user, groups, at); err != nil {
		t.Fatal(err)
	}
}

func TestStoreMemberships(t *testing.T) {
	stores(t, func(t *testing.T, store Store) {
		t.Helper()
		now := time.Now()
		set(t, store, alice, now, Group{eng, "engineering"}, Group{tech, "technology"})
		set(t, store, bob, now, Group{ops, "operations"}, Group{tech, "technology"})

		userGroups := func(user string) []string {
			infos, err := store.UserGroups(t.Context(), user)
			if err != nil || infos == nil {
				t.Fatalf("groups %v, error %v", infos, err)
			}
			return ids(infos)
		}
		if got, want := userGroups(alice), []string{eng + "=engineering", tech + "=technology"}; !slices.Equal(got, want) {
			t.Fatalf("alice %v, want %v", got, want)
		}
		if got := userGroups("nobody"); len(got) != 0 {
			t.Fatalf("unknown user has groups %v", got)
		}

		// Alice leaves engineering, and her token renames technology for everyone.
		set(t, store, alice, now, Group{tech, "tech"})
		if got, want := userGroups(alice), []string{tech + "=tech"}; !slices.Equal(got, want) {
			t.Fatalf("alice %v, want %v", got, want)
		}
		if got, want := userGroups(bob), []string{ops + "=operations", tech + "=tech"}; !slices.Equal(got, want) {
			t.Fatalf("bob %v, want %v", got, want)
		}

		byID, err := store.GroupsByIDs(t.Context(), []string{tech, eng, "not-a-uuid"})
		if err != nil {
			t.Fatal(err)
		}
		// Nobody is in engineering any more, so it's gone.
		if got, want := ids(byID), []string{tech + "=tech"}; !slices.Equal(got, want) {
			t.Fatalf("by ID %v, want %v", got, want)
		}

		set(t, store, bob, now)
		if got := userGroups(bob); len(got) != 0 {
			t.Fatalf("bob left every group but has %v", got)
		}
	})
}

func TestStorePrune(t *testing.T) {
	stores(t, func(t *testing.T, store Store) {
		t.Helper()
		now := time.Now()
		set(t, store, alice, now.Add(-2*Retention), Group{eng, "engineering"})
		set(t, store, bob, now, Group{ops, "operations"})
		if err := store.Prune(t.Context(), now.Add(-Retention)); err != nil {
			t.Fatal(err)
		}
		if infos, _ := store.UserGroups(t.Context(), alice); len(infos) != 0 {
			t.Fatalf("pruned user still has %v", infos)
		}
		if infos, _ := store.UserGroups(t.Context(), bob); len(infos) != 1 {
			t.Fatalf("recent user lost groups: %v", infos)
		}
	})
}

func TestStoreListGroups(t *testing.T) {
	stores(t, func(t *testing.T, store Store) {
		t.Helper()
		now := time.Now()
		set(t, store, alice, now, Group{eng, "engineering"}, Group{tech, "technology"}, Group{other, "50%_off"})
		set(t, store, bob, now, Group{ops, "operations"}, Group{tech, "technology"})

		var names []string
		cursor := ""
		for range 10 {
			page, err := store.ListGroups(t.Context(), authcommon.PageRequest{Limit: 2, Cursor: cursor})
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
		if want := []string{"50%_off", "engineering", "operations", "technology"}; !slices.Equal(names, want) {
			t.Fatalf("names %v, want %v", names, want)
		}

		for filter, want := range map[string][]string{
			"OPER": {"operations"},
			"%":    {"50%_off"},
			"_":    {"50%_off"},
			"n":    {"engineering", "operations", "technology"},
		} {
			page, err := store.ListGroups(t.Context(), authcommon.PageRequest{Limit: 10, NameFilter: filter})
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, item := range page.Items {
				got = append(got, item.Name)
			}
			if !slices.Equal(got, want) || page.NextCursor != "" {
				t.Errorf("filter %q: %v (next %q), want %v", filter, got, page.NextCursor, want)
			}
		}

		for _, bad := range []string{"x", `{"n":"a","i":"not-a-uuid"}`} {
			if _, err := store.ListGroups(t.Context(), authcommon.PageRequest{Limit: 2, Cursor: bad}); !errors.Is(err, authcommon.ErrInvalidCursor) {
				t.Errorf("cursor %q: error %v", bad, err)
			}
		}
	})
}

func (c *countingStore) SetUserGroups(ctx context.Context, userID string, groups []Group, now time.Time) error {
	c.writes++
	return c.Store.SetUserGroups(ctx, userID, groups, now)
}

func TestRecorderSkipsRepeats(t *testing.T) {
	store := &countingStore{Store: NewMemory()}
	recorder := NewRecorder(store)
	now := time.Now()
	recorder.now = func() time.Time { return now }
	record := func(groups ...Group) bool {
		t.Helper()
		wrote, err := recorder.Record(t.Context(), alice, groups)
		if err != nil {
			t.Fatal(err)
		}
		return wrote
	}

	if !record(Group{eng, "engineering"}) || record(Group{eng, "engineering"}) {
		t.Fatal("first write skipped, or repeat written")
	}
	if !record(Group{eng, "engineering"}, Group{tech, "technology"}) {
		t.Fatal("changed groups not written")
	}
	now = now.Add(rewriteInterval)
	if !record(Group{tech, "technology"}, Group{eng, "engineering"}) {
		t.Fatal("unchanged groups not rewritten after the interval")
	}
	if store.writes != 3 {
		t.Fatalf("%d writes, want 3", store.writes)
	}
}
