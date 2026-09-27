package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"unicode/utf8"

	sessdomain "github.com/ecrespo/umbral/internal/sessions/domain"
)

// Block is the wire shape of a block (API Spec §4).
//
// It exists as a struct rather than the map the notifications build, because these are the
// responses a client pages through: a field that silently changes name between the
// notification and the query would be a contract break nobody notices until a client does.
type Block struct {
	ID              string  `json:"id"`
	SessionID       string  `json:"session_id"`
	Origin          string  `json:"origin"`
	ThreadID        *string `json:"thread_id"`
	Command         string  `json:"command"`
	CWD             string  `json:"cwd"`
	Host            string  `json:"host"`
	State           string  `json:"state"`
	ExitCode        *int    `json:"exit_code"`
	StartedAt       int64   `json:"started_at"`
	EndedAt         *int64  `json:"ended_at"`
	DurationMs      *int64  `json:"duration_ms"`
	OutputBytes     int64   `json:"output_bytes"`
	OutputTruncated bool    `json:"output_truncated"`
}

// toWireBlock converts a domain block. Times become UTC epoch milliseconds and the optional
// fields become explicit nulls, both per Art. 6 and API Spec §4.
func toWireBlock(b sessdomain.Block) Block {
	out := Block{
		ID: b.ID, SessionID: b.SessionID, Origin: string(b.Origin),
		Command: b.Command, CWD: b.CWD, Host: b.Host, State: string(b.State),
		ExitCode:        b.ExitCode,
		StartedAt:       b.StartedAt.UTC().UnixMilli(),
		DurationMs:      b.DurationMs(),
		OutputBytes:     b.OutputBytes,
		OutputTruncated: b.OutputTruncated,
	}
	if b.ThreadID != "" {
		thread := b.ThreadID
		out.ThreadID = &thread
	}
	if b.EndedAt != nil {
		ended := b.EndedAt.UTC().UnixMilli()
		out.EndedAt = &ended
	}
	return out
}

type listBlocksParams struct {
	SessionID string `json:"session_id" api:"optional"`
	ThreadID  string `json:"thread_id" api:"optional"`
	Origin    string `json:"origin" api:"optional"`
	State     string `json:"state" api:"optional"`
	ExitCode  *int   `json:"exit_code"`
	// §3 gives both a default, and the handlers honour it: an absent limit becomes 50
	// and an absent cursor is the first page. Publishing them as required would declare
	// invalid a request this daemon serves — `umbral-tui` sends exactly one of those.
	Limit  int    `json:"limit" api:"optional"`
	Cursor string `json:"cursor" api:"optional"`
}

// blockPage is the paged response of API Spec §3. next_cursor is null on the last page, not
// absent: a client looping until the key disappears would loop forever.
type blockPage struct {
	Items      []Block `json:"items"`
	NextCursor *string `json:"next_cursor"`
}

func handleBlockList(ctx context.Context, c *conn, raw json.RawMessage) (any, error) {
	reader := c.server.cfg.Blocks
	if reader == nil {
		return nil, fmt.Errorf("%w: block.list", ErrNotImplemented)
	}

	var params listBlocksParams
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &params); err != nil {
			return nil, ValidationError("block.list parameters are not an object")
		}
	}

	cursor, err := sessdomain.ParseCursor(params.Cursor)
	if err != nil {
		return nil, err
	}

	page, err := reader.List(ctx, sessdomain.BlockFilter{
		SessionID: params.SessionID,
		ThreadID:  params.ThreadID,
		Origin:    sessdomain.BlockOrigin(params.Origin),
		State:     sessdomain.BlockState(params.State),
		ExitCode:  params.ExitCode,
		Limit:     params.Limit,
		Cursor:    cursor,
	})
	if err != nil {
		return nil, err
	}

	return blockPage{Items: toWireBlocks(page.Items), NextCursor: cursorToken(page.NextCursor)}, nil
}

type getBlockParams struct {
	BlockID   string `json:"block_id"`
	SessionID string `json:"session_id" api:"optional"`
	// An absent include returns the block with no output section, which is what §5.13
	// means by "none".
	Include string `json:"include" api:"optional"`
}

// blockResult is `block.get`'s response: the block, plus whichever output was asked for.
type blockResult struct {
	Block
	// OutputPlain is present for include "plain" (REQ-BLK-007).
	OutputPlain *string `json:"output_plain,omitempty"`
	// OutputRawB64 is present for include "raw", base64 as API Spec §3 requires of every
	// binary payload.
	OutputRawB64 *string `json:"output_raw_b64,omitempty"`
	// OutputRawTruncated says the raw output stops short of the block's own total. It
	// accompanies output_raw_b64 and nothing else.
	OutputRawTruncated *bool `json:"output_raw_truncated,omitempty"`
	// OutputResponseTruncatedBytes is how many bytes of the stored output this response
	// leaves out to fit the connection's frame limit (REQ-API-005). Absent when nothing was
	// left out. It is not output_truncated, which says the stored capture itself was cut:
	// this one is what raising the limit would bring back, that one is gone.
	OutputResponseTruncatedBytes *int64 `json:"output_response_truncated_bytes,omitempty"`
}

func handleBlockGet(ctx context.Context, c *conn, raw json.RawMessage) (any, error) {
	reader := c.server.cfg.Blocks
	if reader == nil {
		return nil, fmt.Errorf("%w: block.get", ErrNotImplemented)
	}

	var params getBlockParams
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &params); err != nil {
			return nil, ValidationError("block.get parameters are not an object")
		}
	}
	if params.BlockID == "" {
		return nil, ValidationError("block.get needs a block_id",
			ErrorField{Field: "block_id", Issue: `a block id, or "last"`})
	}

	block, output, err := reader.Get(ctx, params.BlockID, params.SessionID,
		sessdomain.Include(params.Include))
	if err != nil {
		return nil, err
	}

	result := blockResult{Block: toWireBlock(block)}
	switch sessdomain.Include(params.Include) {
	case sessdomain.IncludePlain:
		plain := output.Plain
		result.OutputPlain = &plain
	case sessdomain.IncludeRaw:
		encoded := base64.StdEncoding.EncodeToString(output.Raw)
		truncated := output.RawTruncated
		result.OutputRawB64 = &encoded
		result.OutputRawTruncated = &truncated
	}
	return fitBlockResult(result, output, c.limit.Load())
}

// envelopeReserve is what fitBlockResult leaves for the response around the result: the
// `jsonrpc`, `id` and `result` members and the `\n`. An id longer than the reserve leaves the
// encoder to answer RESULT_TOO_LARGE, which is still a frame the client can read.
const envelopeReserve = 4 << 10

// fitBlockResult shortens block.get's one output field so the response fits the frame
// limit, instead of letting the encoder refuse it whole (REQ-API-005): a user asking for a
// block's output would rather have most of it than an error. `include` selects plain or raw,
// never both, so at most one field is cut:
//
//   - raw on a 3-byte boundary before base64, so the encoded text is a prefix of what a
//     longer answer would have carried;
//   - plain on a UTF-8 boundary, measured as JSON rather than as Go bytes, because escaping
//     can double a transcript full of quotes and control characters.
func fitBlockResult(result blockResult, output sessdomain.BlockOutput, limit int64) (any, error) {
	budget := int(limit) - envelopeReserve
	full, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	if len(full) <= budget || (result.OutputPlain == nil && result.OutputRawB64 == nil) {
		return result, nil
	}

	// Measure everything but the output, with the new field present at its widest.
	placeholder := int64(1) << 40
	result.OutputResponseTruncatedBytes = &placeholder
	empty := ""
	if result.OutputPlain != nil {
		result.OutputPlain = &empty
	} else {
		result.OutputRawB64 = &empty
	}
	frame, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	room := budget - len(frame)
	if room < 0 {
		return result, nil // nothing fits; the encoder answers RESULT_TOO_LARGE
	}

	var omitted int64
	if result.OutputPlain != nil {
		kept := plainPrefix(output.Plain, room)
		result.OutputPlain = &kept
		omitted = int64(len(output.Plain) - len(kept))
	} else {
		n := min(room/4*3, len(output.Raw))
		encoded := base64.StdEncoding.EncodeToString(output.Raw[:n])
		result.OutputRawB64 = &encoded
		omitted = int64(len(output.Raw) - n)
	}
	result.OutputResponseTruncatedBytes = &omitted
	return result, nil
}

// plainPrefix is the longest prefix of text, cut on a rune boundary, whose JSON string
// contents take at most room bytes. Found by bisection over the encoded length: the one
// measure that cannot disagree with the encoder is the encoder.
func plainPrefix(text string, room int) string {
	fits := func(n int) bool {
		encoded, _ := json.Marshal(text[:n])
		return len(encoded)-2 <= room
	}
	// fits(runeStart(n)) only ever turns from true to false as n grows, so bisect on n.
	lo, hi := 0, len(text)
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if fits(runeStart(text, mid)) {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	return text[:runeStart(text, lo)]
}

// runeStart moves n back to the start of the rune it falls in.
func runeStart(text string, n int) int {
	for n > 0 && n < len(text) && !utf8.RuneStart(text[n]) {
		n--
	}
	return n
}

type searchBlocksParams struct {
	Query     string `json:"query"`
	SessionID string `json:"session_id" api:"optional"`
	Limit     int    `json:"limit" api:"optional"`
	Cursor    string `json:"cursor" api:"optional"`
}

type searchHit struct {
	Block   Block  `json:"block"`
	Snippet string `json:"snippet"`
}

type searchPage struct {
	Items      []searchHit `json:"items"`
	NextCursor *string     `json:"next_cursor"`
}

func handleBlockSearch(ctx context.Context, c *conn, raw json.RawMessage) (any, error) {
	reader := c.server.cfg.Blocks
	if reader == nil {
		return nil, fmt.Errorf("%w: block.search", ErrNotImplemented)
	}

	var params searchBlocksParams
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &params); err != nil {
			return nil, ValidationError("block.search parameters are not an object")
		}
	}

	cursor, err := sessdomain.ParseCursor(params.Cursor)
	if err != nil {
		return nil, err
	}

	page, err := reader.Search(ctx, sessdomain.SearchQuery{
		Query:     params.Query,
		SessionID: params.SessionID,
		Limit:     params.Limit,
		Cursor:    cursor,
	})
	if err != nil {
		return nil, err
	}

	hits := make([]searchHit, 0, len(page.Items))
	for _, hit := range page.Items {
		hits = append(hits, searchHit{Block: toWireBlock(hit.Block), Snippet: hit.Snippet})
	}
	return searchPage{Items: hits, NextCursor: cursorToken(page.NextCursor)}, nil
}

// toWireBlocks converts a page's rows. The empty slice is deliberate: `items` must be `[]`
// and never `null`, because a client iterating the result should not have to special-case
// an empty page.
func toWireBlocks(blocks []sessdomain.Block) []Block {
	out := make([]Block, 0, len(blocks))
	for _, block := range blocks {
		out = append(out, toWireBlock(block))
	}
	return out
}

// cursorToken renders a cursor for the wire, or nil when this was the last page.
func cursorToken(cursor sessdomain.Cursor) *string {
	if cursor.IsZero() {
		return nil
	}
	token := cursor.String()
	return &token
}
