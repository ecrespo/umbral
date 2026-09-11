package domain

import (
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Pagination limits from API Spec §3.
const (
	// MinPageLimit and MaxPageLimit bound how many rows one page may carry.
	MinPageLimit = 1
	MaxPageLimit = 200
	// DefaultPageLimit is what a request that names no limit gets.
	DefaultPageLimit = 50
)

// Search query limits from API Spec §5.12.
const (
	MinSearchQuery = 1
	MaxSearchQuery = 256
)

// BlockLast is the reserved id that `block.get` accepts in place of a real one
// (API Spec §5.11, REQ-CLI-002).
const BlockLast = "last"

// Include says how much of a block's output `block.get` should return (API Spec §5.11).
type Include string

const (
	// IncludeNone returns the block's metadata only.
	IncludeNone Include = "none"
	// IncludePlain returns the transcript REQ-BLK-007 stores.
	IncludePlain Include = "plain"
	// IncludeRaw returns the stored chunks, escapes and all.
	IncludeRaw Include = "raw"
)

// MaxRawOutputBytes is how much raw output one `block.get` may return.
//
// A block may hold 16 MiB of raw output, and base64 turns that into 21 MiB, five times the
// 4 MiB message limit of API Spec §8. Something has to give, and silently cutting the bytes
// would let a client believe it had the whole block. The response therefore carries at most
// this much and says so, which leaves room inside the message for the block's own fields.
const MaxRawOutputBytes = 2 << 20

// BlockFilter is the `block.list` query (API Spec §5.10).
//
// Every field is optional: an empty filter lists the whole history, newest first.
type BlockFilter struct {
	SessionID string
	ThreadID  string
	Origin    BlockOrigin
	State     BlockState
	ExitCode  *int
	Limit     int
	Cursor    Cursor
}

// Validate bounds the limit and rejects values the schema's CHECK constraints would.
//
// It normalises rather than only refusing: a request with no limit gets the default, which
// is what API Spec §3 says a client may omit.
func (f *BlockFilter) Validate() error {
	if f.Limit == 0 {
		f.Limit = DefaultPageLimit
	}
	if f.Limit < MinPageLimit || f.Limit > MaxPageLimit {
		return fmt.Errorf("%w: limit is %d, must be between %d and %d",
			ErrValidation, f.Limit, MinPageLimit, MaxPageLimit)
	}
	switch f.Origin {
	case "", OriginUser, OriginAgent:
	default:
		return fmt.Errorf("%w: unknown origin %q", ErrValidation, f.Origin)
	}
	switch f.State {
	case "", BlockRunning, BlockInteractive, BlockFinished, BlockAbandoned:
	default:
		return fmt.Errorf("%w: unknown state %q", ErrValidation, f.State)
	}
	return nil
}

// SearchQuery is the `block.search` query (API Spec §5.12, REQ-BLK-006).
type SearchQuery struct {
	// Query is FTS5 syntax, passed to SQLite as written.
	Query     string
	SessionID string
	Limit     int
	Cursor    Cursor
}

// Validate bounds the query and the limit.
func (q *SearchQuery) Validate() error {
	if length := len(q.Query); length < MinSearchQuery || length > MaxSearchQuery {
		return fmt.Errorf("%w: query is %d characters, must be between %d and %d",
			ErrValidation, length, MinSearchQuery, MaxSearchQuery)
	}
	if q.Limit == 0 {
		q.Limit = DefaultPageLimit
	}
	if q.Limit < MinPageLimit || q.Limit > MaxPageLimit {
		return fmt.Errorf("%w: limit is %d, must be between %d and %d",
			ErrValidation, q.Limit, MinPageLimit, MaxPageLimit)
	}
	return nil
}

// Cursor marks where a page ended (API Spec §3).
//
// It has two shapes, because the two paged methods are ordered differently and a cursor has
// to name a position in the ordering it belongs to.
//
// `block.list` is ordered by start time descending, and start times collide: a script that
// runs ten commands in a millisecond gives them all the same one. The block id breaks the
// tie, and because ids are ULIDs they sort the same way time does. Carrying both is what
// makes a page boundary land between two rows rather than in the middle of a group.
//
// `block.search` is ordered by insertion position instead, which is what lets SQLite walk
// the full-text index backwards and stop at the limit rather than sort every match. That
// position is what a search cursor carries.
type Cursor struct {
	// StartedAt and BlockID are the list shape.
	StartedAt time.Time
	BlockID   string
	// Row is the search shape: the position of the last hit in insertion order.
	Row int64
}

// SearchCursor builds the cursor `block.search` pages with.
func SearchCursor(row int64) Cursor { return Cursor{Row: row} }

// IsZero reports whether this is the first page.
func (c Cursor) IsZero() bool { return c.BlockID == "" && c.Row == 0 }

// String encodes the cursor as the opaque token the client echoes back. Opaque means the
// client must not read it, not that it has to be unreadable; base64 keeps it out of the
// way of JSON without pretending to be a secret. The leading letter is the shape, so a
// cursor from one method handed to the other is refused rather than misread.
func (c Cursor) String() string {
	var raw string
	switch {
	case c.IsZero():
		return ""
	case c.Row != 0:
		raw = "r|" + strconv.FormatInt(c.Row, 10)
	default:
		raw = "t|" + strconv.FormatInt(c.StartedAt.UTC().UnixMilli(), 10) + "|" + c.BlockID
	}
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// ParseCursor reads a token back. An empty token is the first page.
func ParseCursor(token string) (Cursor, error) {
	if token == "" {
		return Cursor{}, nil
	}

	decoded, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return Cursor{}, badCursor()
	}
	shape, rest, found := strings.Cut(string(decoded), "|")
	if !found {
		return Cursor{}, badCursor()
	}

	switch shape {
	case "r":
		row, err := strconv.ParseInt(rest, 10, 64)
		if err != nil || row == 0 {
			return Cursor{}, badCursor()
		}
		return Cursor{Row: row}, nil
	case "t":
		millis, id, found := strings.Cut(rest, "|")
		if !found || id == "" {
			return Cursor{}, badCursor()
		}
		value, err := strconv.ParseInt(millis, 10, 64)
		if err != nil {
			return Cursor{}, badCursor()
		}
		return Cursor{StartedAt: time.UnixMilli(value).UTC(), BlockID: id}, nil
	default:
		return Cursor{}, badCursor()
	}
}

func badCursor() error {
	return fmt.Errorf("%w: cursor is not a valid token", ErrValidation)
}

// BlockPage is one page of `block.list` (API Spec §3).
type BlockPage struct {
	Items      []Block
	NextCursor Cursor
}

// SearchHit is one result of `block.search`: the block and the piece of text that matched.
type SearchHit struct {
	Block Block
	// Snippet is the matching text with the match marked, as FTS5 produced it.
	Snippet string
}

// SearchPage is one page of `block.search`.
type SearchPage struct {
	Items      []SearchHit
	NextCursor Cursor
}

// BlockOutput is what `block.get` returns alongside the block.
type BlockOutput struct {
	// Plain is the transcript, set when Include was IncludePlain.
	Plain string
	// Raw is the stored chunks reassembled, set when Include was IncludeRaw.
	Raw []byte
	// RawTruncated reports that Raw stops at MaxRawOutputBytes and the block holds more.
	RawTruncated bool
}
