package cast

import (
	"fmt"
	"time"

	"connectrpc.com/connect"

	"github.com/stupside/castor/internal/registry"
)

// linger keeps a finished cast findable, so a watcher that arrives late still learns how it ended.
const linger = 5 * time.Minute

// sessions is the casts this server runs, by id; every service finds its cast here.
type sessions = registry.Registry[*session]

func newSessions() *sessions { return registry.New[*session](linger) }

// find is the cast id names, refused as unknown when there is none.
func find(all *sessions, id string) (*session, error) {
	s, ok := all.Find(id)
	if !ok {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("no cast %q", id))
	}
	return s, nil
}
