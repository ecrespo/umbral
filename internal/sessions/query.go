package sessions

import (
	"context"
	"errors"
	"fmt"

	"github.com/ecrespo/umbral/internal/sessions/domain"
	"github.com/ecrespo/umbral/internal/sessions/ports"
)

// Reader answers the history questions of API Spec §5.10 through §5.12.
//
// It is a second service rather than more methods on Service because the two have nothing
// in common: Service owns live PTYs and their goroutines, while this one owns no state at
// all and only validates a query before handing it to the store. Keeping them apart is also
// what lets `umb`, which may read blocks but not drive terminals, be wired to one and not
// the other.
type Reader struct {
	blocks ports.BlockReader
}

// ReaderConfig wires the query service.
//
// It is a struct for the same reason Config is, and the reason is worth writing down: the
// adapter is injected here by cmd, the only place Art. 3 allows modules to be wired, and
// go-arch-lint's deep scan reads a concrete adapter type passed straight into a service
// constructor as the adapter depending on the service. Every constructor in this module
// therefore takes its dependencies in a struct, which is also what keeps adding one from
// being a breaking change.
type ReaderConfig struct {
	Blocks ports.BlockReader
}

// NewReader builds the query service.
func NewReader(cfg ReaderConfig) (*Reader, error) {
	if cfg.Blocks == nil {
		return nil, errors.New("sessions: a BlockReader is required")
	}
	return &Reader{blocks: cfg.Blocks}, nil
}

// List pages through the history, newest first (API Spec §5.10).
func (r *Reader) List(ctx context.Context, filter domain.BlockFilter) (domain.BlockPage, error) {
	if err := filter.Validate(); err != nil {
		return domain.BlockPage{}, err
	}
	return r.blocks.List(ctx, filter)
}

// Search runs an FTS5 query over commands and transcripts (REQ-BLK-006).
func (r *Reader) Search(ctx context.Context, query domain.SearchQuery) (domain.SearchPage, error) {
	if err := query.Validate(); err != nil {
		return domain.SearchPage{}, err
	}
	return r.blocks.Search(ctx, query)
}

// Get returns one block and, when asked, its output (API Spec §5.11, REQ-CLI-002).
//
// The reserved id `last` resolves to the most recent closed block, of one session when
// sessionID is given and of the whole history otherwise. That is the shape `umb block last`
// needs: the CLI knows which session it was launched from, and a person asking from outside
// any session means "the last thing that ran".
func (r *Reader) Get(ctx context.Context, id, sessionID string, include domain.Include) (domain.Block, domain.BlockOutput, error) {
	switch include {
	case "", domain.IncludeNone, domain.IncludePlain, domain.IncludeRaw:
	default:
		return domain.Block{}, domain.BlockOutput{},
			fmt.Errorf("%w: include is %q, must be none, plain or raw", domain.ErrValidation, include)
	}

	var (
		block domain.Block
		err   error
	)
	if id == domain.BlockLast {
		block, err = r.blocks.Last(ctx, sessionID)
	} else {
		block, err = r.blocks.Get(ctx, id)
	}
	if err != nil {
		return domain.Block{}, domain.BlockOutput{}, err
	}
	// A block id that exists but belongs to another session is not this session's block,
	// and answering with it would let one session read another's history through a
	// parameter meant to narrow the search. The id is deliberately left out of the error:
	// "not found" and "found, but not yours" must be indistinguishable, or the refusal
	// becomes a way to confirm that an id exists.
	if sessionID != "" && block.SessionID != sessionID {
		return domain.Block{}, domain.BlockOutput{},
			fmt.Errorf("%w: no such block in this session", domain.ErrNotFound)
	}

	output, err := r.output(ctx, block.ID, include)
	if err != nil {
		return domain.Block{}, domain.BlockOutput{}, err
	}
	return block, output, nil
}

func (r *Reader) output(ctx context.Context, id string, include domain.Include) (domain.BlockOutput, error) {
	switch include {
	case domain.IncludePlain:
		plain, err := r.blocks.Plain(ctx, id)
		if err != nil {
			return domain.BlockOutput{}, err
		}
		return domain.BlockOutput{Plain: plain}, nil
	case domain.IncludeRaw:
		raw, truncated, err := r.blocks.Raw(ctx, id, domain.MaxRawOutputBytes)
		if err != nil {
			return domain.BlockOutput{}, err
		}
		return domain.BlockOutput{Raw: raw, RawTruncated: truncated}, nil
	default:
		return domain.BlockOutput{}, nil
	}
}

// Reader implements the module's history port.
var _ ports.Blocks = (*Reader)(nil)
