package groups

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strings"
	"sync"
	"time"
)

const (
	// rewriteInterval is how often unchanged groups are written again, so that Retention counts
	// from when the user was last active rather than when their groups last changed.
	rewriteInterval = time.Hour

	// maxRemembered bounds the record of recent writes. Forgetting it only costs extra writes.
	maxRemembered = 10000
)

// Recorder saves users' groups, skipping the write when the same groups were saved recently.
// Obot reads the session on every request, so most calls repeat the last one.
type Recorder struct {
	store Store
	now   func() time.Time

	mu   sync.Mutex
	last map[string]written
}

type written struct {
	digest string
	at     time.Time
}

func NewRecorder(store Store) *Recorder {
	return &Recorder{store: store, now: time.Now, last: map[string]written{}}
}

// Record saves the user's groups, and reports whether it wrote them.
func (r *Recorder) Record(ctx context.Context, userID string, groups []Group) (bool, error) {
	now := r.now()
	d := digest(groups)
	r.mu.Lock()
	prev, ok := r.last[userID]
	r.mu.Unlock()
	if ok && prev.digest == d && now.Sub(prev.at) < rewriteInterval {
		return false, nil
	}

	if err := r.store.SetUserGroups(ctx, userID, groups, now); err != nil {
		return false, err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.last) >= maxRemembered {
		clear(r.last)
	}
	r.last[userID] = written{digest: d, at: now}
	return true, nil
}

func digest(groups []Group) string {
	lines := make([]string, 0, len(groups))
	for _, g := range groups {
		lines = append(lines, g.ID+"\x00"+g.Name)
	}
	slices.Sort(lines)
	sum := sha256.Sum256([]byte(strings.Join(lines, "\x01")))
	return hex.EncodeToString(sum[:])
}
