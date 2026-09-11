package store

import (
	"crypto/rand"
	"fmt"
	"time"

	"github.com/oklog/ulid/v2"
)

// ID prefixes from API Spec §3 and Constitution Art. 6. Every public identifier is a
// type-prefixed ULID, so a value carries its own type and two tables can never collide.
const (
	PrefixSession    = "ses"
	PrefixBlock      = "blk"
	PrefixThread     = "thr"
	PrefixMessage    = "msg"
	PrefixToolCall   = "tc"
	PrefixApproval   = "apr"
	PrefixMcpServer  = "mcp"
	PrefixConnection = "con"
)

// NewID returns a type-prefixed ULID such as "blk_01J9Z3K8T2QH6W4V5X7Y8Z9A0B".
//
// ULIDs are lexicographically sortable by creation time, which is why the schema can
// page through history on the primary key without a secondary sort.
//
// It lives in store because the ID format is a data convention: the CHECK constraints
// that enforce these prefixes are in the migrations next door.
func NewID(prefix string) string {
	return prefix + "_" + ulid.MustNew(ulid.Timestamp(time.Now()), rand.Reader).String()
}

// ParsePrefix reports the type prefix of an identifier, so a handler can reject an id of
// the wrong kind before it reaches the database.
func ParsePrefix(id string) (string, error) {
	for i := range len(id) {
		if id[i] == '_' {
			if i == 0 {
				break
			}
			return id[:i], nil
		}
	}
	return "", fmt.Errorf("store: %q is not a prefixed identifier", id)
}
