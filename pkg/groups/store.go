package groups

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/obot-platform/enterprise-providers/authcommon"
	"github.com/obot-platform/providers/auth-providers-common/pkg/state"
	"github.com/solidDoWant/obot-authentik-provider/pkg/profile"
)

const (
	// Retention is how long a user's groups are kept after they were last seen. It matches
	// authentik's default refresh token validity: a session older than that can't be refreshed,
	// so its groups can't be current.
	Retention = 30 * 24 * time.Hour

	// table holds one row per user and group. Its prefix is the provider's postgresTablePrefix.
	table = "authentik_group_memberships"
)

// Store keeps the groups of every user seen in an ID token.
type Store interface {
	// SetUserGroups replaces the user's groups, and updates the names of those groups for every
	// user.
	SetUserGroups(ctx context.Context, userID string, groups []Group, now time.Time) error
	// UserGroups returns the user's groups, or none for a user it hasn't seen.
	UserGroups(ctx context.Context, userID string) (state.GroupInfoList, error)
	// ListGroups returns one page of the groups that any user is in, sorted by name.
	ListGroups(ctx context.Context, req authcommon.PageRequest) (authcommon.PageResult, error)
	// GroupsByIDs returns the groups it knows among ids, which are authentik UUIDs.
	GroupsByIDs(ctx context.Context, ids []string) (state.GroupInfoList, error)
	// Prune forgets users last seen before the given time.
	Prune(ctx context.Context, before time.Time) error
}

// cursor is the position after the last group of a page.
type cursor struct {
	Name string `json:"n"`
	ID   string `json:"i"`
}

// Memory is a Store that lives only as long as the process. Obot restarts providers when it
// restarts, and every replica has its own, so it's only for a single replica without Postgres.
type Memory struct {
	mu    sync.Mutex
	users map[string]memoryUser
	names map[string]string
}

type memoryUser struct {
	groups []string
	seen   time.Time
}

// Postgres is a Store in Obot's own database, shared by every replica.
type Postgres struct {
	pool *pgxpool.Pool
}

func decodeCursor(raw string) (*cursor, error) {
	if raw == "" {
		return nil, nil
	}
	var c cursor
	if err := json.Unmarshal([]byte(raw), &c); err != nil || !profile.IsUUID(c.ID) {
		return nil, fmt.Errorf("%w: invalid group position", authcommon.ErrInvalidCursor)
	}
	return &c, nil
}

func encodeCursor(g Group) string {
	raw, _ := json.Marshal(cursor{Name: g.Name, ID: g.ID})
	return string(raw)
}

// page cuts sorted candidates, which hold up to one more than the page size, into a page.
func page(candidates []Group, limit int) authcommon.PageResult {
	result := authcommon.PageResult{Items: make(state.GroupInfoList, 0, min(len(candidates), limit))}
	for i, g := range candidates {
		if i == limit {
			result.NextCursor = encodeCursor(candidates[i-1])
			break
		}
		result.Items = append(result.Items, g.Info())
	}
	return result
}

func validPageRequest(req authcommon.PageRequest) error {
	if req.Limit <= 0 || req.Limit > authcommon.MaxGroupPageSize {
		return errors.New("invalid page size")
	}
	return nil
}

func NewMemory() *Memory {
	return &Memory{users: map[string]memoryUser{}, names: map[string]string{}}
}

func (m *Memory) SetUserGroups(_ context.Context, userID string, groups []Group, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	ids := make([]string, 0, len(groups))
	for _, g := range groups {
		ids = append(ids, g.ID)
		m.names[g.ID] = g.Name
	}
	m.users[userID] = memoryUser{groups: ids, seen: now}
	m.forgetUnusedNames()
	return nil
}

func (m *Memory) UserGroups(_ context.Context, userID string) (state.GroupInfoList, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	infos := state.GroupInfoList{}
	for _, id := range m.users[userID].groups {
		infos = append(infos, Group{ID: id, Name: m.names[id]}.Info())
	}
	slices.SortFunc(infos, func(a, b state.GroupInfo) int { return strings.Compare(a.ID, b.ID) })
	return infos, nil
}

func (m *Memory) ListGroups(_ context.Context, req authcommon.PageRequest) (authcommon.PageResult, error) {
	if err := validPageRequest(req); err != nil {
		return authcommon.PageResult{}, err
	}
	after, err := decodeCursor(req.Cursor)
	if err != nil {
		return authcommon.PageResult{}, err
	}
	m.mu.Lock()
	all := make([]Group, 0, len(m.names))
	for id, name := range m.names {
		all = append(all, Group{ID: id, Name: name})
	}
	m.mu.Unlock()

	slices.SortFunc(all, compareGroups)
	filter := strings.ToLower(req.NameFilter)
	candidates := []Group{}
	for _, g := range all {
		if after != nil && compareGroups(g, Group{ID: after.ID, Name: after.Name}) <= 0 {
			continue
		}
		if strings.Contains(strings.ToLower(g.Name), filter) {
			candidates = append(candidates, g)
			if len(candidates) > req.Limit {
				break
			}
		}
	}
	return page(candidates, req.Limit), nil
}

func (m *Memory) GroupsByIDs(_ context.Context, ids []string) (state.GroupInfoList, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	infos := state.GroupInfoList{}
	for _, id := range ids {
		if name, ok := m.names[id]; ok {
			infos = append(infos, Group{ID: id, Name: name}.Info())
		}
	}
	return infos, nil
}

func (m *Memory) Prune(_ context.Context, before time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, u := range m.users {
		if u.seen.Before(before) {
			delete(m.users, id)
		}
	}
	m.forgetUnusedNames()
	return nil
}

// forgetUnusedNames drops groups that no user is in any more. The caller holds the lock.
func (m *Memory) forgetUnusedNames() {
	used := map[string]bool{}
	for _, u := range m.users {
		for _, id := range u.groups {
			used[id] = true
		}
	}
	for id := range m.names {
		if !used[id] {
			delete(m.names, id)
		}
	}
}

func compareGroups(a, b Group) int {
	if c := strings.Compare(a.Name, b.Name); c != 0 {
		return c
	}
	return strings.Compare(a.ID, b.ID)
}

// NewPostgres connects to the database and creates the table if needed.
func NewPostgres(ctx context.Context, dsn string, maxConns int) (*Postgres, error) {
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse the Postgres connection string: %w", err)
	}
	if maxConns > 0 {
		config.MaxConns = int32(min(maxConns, 1<<31-1))
	}
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("connect to Postgres: %w", err)
	}
	_, err = pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS `+table+` (
			user_id text NOT NULL,
			group_id uuid NOT NULL,
			group_name text NOT NULL,
			seen_at timestamptz NOT NULL,
			PRIMARY KEY (user_id, group_id)
		);
		CREATE INDEX IF NOT EXISTS `+table+`_by_name ON `+table+` (group_name, group_id);
		CREATE INDEX IF NOT EXISTS `+table+`_by_seen ON `+table+` (seen_at);`)
	if err != nil {
		pool.Close()
		return nil, fmt.Errorf("create the %s table: %w", table, err)
	}
	return &Postgres{pool: pool}, nil
}

func (p *Postgres) Close() { p.pool.Close() }

func (p *Postgres) SetUserGroups(ctx context.Context, userID string, groups []Group, now time.Time) error {
	ids := make([]string, 0, len(groups))
	names := make([]string, 0, len(groups))
	for _, g := range groups {
		ids = append(ids, g.ID)
		names = append(names, g.Name)
	}
	return pgx.BeginFunc(ctx, p.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM `+table+` WHERE user_id = $1 AND NOT (group_id = ANY($2::uuid[]))`, userID, ids); err != nil {
			return fmt.Errorf("remove old groups: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO `+table+` (user_id, group_id, group_name, seen_at)
			SELECT $1, id, name, $4 FROM unnest($2::uuid[], $3::text[]) AS g(id, name)
			ON CONFLICT (user_id, group_id) DO UPDATE SET group_name = excluded.group_name, seen_at = excluded.seen_at`,
			userID, ids, names, now); err != nil {
			return fmt.Errorf("save groups: %w", err)
		}
		// A rename reaches every member at once, so a group never has two names.
		if _, err := tx.Exec(ctx, `
			UPDATE `+table+` AS m SET group_name = g.name
			FROM unnest($1::uuid[], $2::text[]) AS g(id, name)
			WHERE m.group_id = g.id AND m.group_name <> g.name`, ids, names); err != nil {
			return fmt.Errorf("rename groups: %w", err)
		}
		return nil
	})
}

func (p *Postgres) UserGroups(ctx context.Context, userID string) (state.GroupInfoList, error) {
	return p.query(ctx, `SELECT group_id::text, group_name FROM `+table+` WHERE user_id = $1 ORDER BY group_id`, userID)
}

func (p *Postgres) ListGroups(ctx context.Context, req authcommon.PageRequest) (authcommon.PageResult, error) {
	if err := validPageRequest(req); err != nil {
		return authcommon.PageResult{}, err
	}
	after, err := decodeCursor(req.Cursor)
	if err != nil {
		return authcommon.PageResult{}, err
	}
	var afterName, afterID *string
	if after != nil {
		afterName, afterID = &after.Name, &after.ID
	}
	rows, err := p.pool.Query(ctx, `
		SELECT DISTINCT group_id::text, group_name FROM `+table+`
		WHERE group_name ILIKE '%' || $1 || '%' ESCAPE '\'
			AND ($2::text IS NULL OR (group_name, group_id) > ($2::text, $3::uuid))
		ORDER BY group_name, group_id::text
		LIMIT $4`, escapeLike(req.NameFilter), afterName, afterID, req.Limit+1)
	if err != nil {
		return authcommon.PageResult{}, fmt.Errorf("list groups: %w", err)
	}
	candidates, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Group, error) {
		var g Group
		return g, row.Scan(&g.ID, &g.Name)
	})
	if err != nil {
		return authcommon.PageResult{}, fmt.Errorf("list groups: %w", err)
	}
	return page(candidates, req.Limit), nil
}

func (p *Postgres) GroupsByIDs(ctx context.Context, ids []string) (state.GroupInfoList, error) {
	valid := make([]string, 0, len(ids))
	for _, id := range ids {
		if profile.IsUUID(id) {
			valid = append(valid, id)
		}
	}
	return p.query(ctx, `
		SELECT DISTINCT ON (group_id) group_id::text, group_name FROM `+table+`
		WHERE group_id = ANY($1::uuid[]) ORDER BY group_id, seen_at DESC`, valid)
}

func (p *Postgres) Prune(ctx context.Context, before time.Time) error {
	if _, err := p.pool.Exec(ctx, `DELETE FROM `+table+` WHERE seen_at < $1`, before); err != nil {
		return fmt.Errorf("prune groups: %w", err)
	}
	return nil
}

func (p *Postgres) query(ctx context.Context, sql string, args ...any) (state.GroupInfoList, error) {
	rows, err := p.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("read groups: %w", err)
	}
	infos, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (state.GroupInfo, error) {
		var g Group
		err := row.Scan(&g.ID, &g.Name)
		return g.Info(), err
	})
	if err != nil {
		return nil, fmt.Errorf("read groups: %w", err)
	}
	if infos == nil {
		infos = state.GroupInfoList{}
	}
	return infos, nil
}

// escapeLike makes a name filter match literally inside an ILIKE pattern.
func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}
