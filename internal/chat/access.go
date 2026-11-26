package chat

import (
	"errors"
	"fmt"
	"strings"

	"github.com/ayopedro/go-obs-agent/internal/audit"
)

// ErrForbidden is returned by Submit when the requester is not allowed.
var ErrForbidden = errors.New("requester not allowed")

// Access limits who can start investigations. A request must match every
// non-empty list. Platform user and channel IDs are compared exactly.
type Access struct {
	users    map[string]bool
	channels map[string]bool
}

// ParseAccess builds a policy from comma-separated ID lists. It fails closed:
// both lists empty is an error unless allowAll is set, which returns nil
// (no restriction).
func ParseAccess(users, channels string, allowAll bool) (*Access, error) {
	a := &Access{users: splitIDs(users), channels: splitIDs(channels)}
	if len(a.users) == 0 && len(a.channels) == 0 {
		if allowAll {
			return nil, nil
		}
		return nil, fmt.Errorf("set ALLOWED_USERS or ALLOWED_CHANNELS, or ACCESS_ALLOW_ALL=true to allow everyone in the configured workspace/tenant")
	}
	return a, nil
}

func splitIDs(s string) map[string]bool {
	out := map[string]bool{}
	for _, id := range strings.Split(s, ",") {
		if id = strings.TrimSpace(id); id != "" {
			out[id] = true
		}
	}
	return out
}

// Permits reports whether the actor may start an investigation. A nil policy permits everyone.
func (a *Access) Permits(actor audit.Actor) bool {
	if a == nil {
		return true
	}
	if len(a.users) > 0 && !a.users[actor.User] {
		return false
	}
	if len(a.channels) > 0 && !a.channels[actor.Channel] {
		return false
	}
	return true
}
