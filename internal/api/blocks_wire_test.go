package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/ecrespo/umbral/internal/bus"
	sessdomain "github.com/ecrespo/umbral/internal/sessions/domain"
	sessports "github.com/ecrespo/umbral/internal/sessions/ports"
)

// fakeBlocks is a ports.Blocks that answers from a slice.
//
// The resolution rules it would otherwise duplicate, which block is "last" and how a cursor
// advances, are not reimplemented here: they belong to the store and are tested against
// real SQL. What these tests are about is the wire, which is the one thing neither the store
// nor the integration package can reach.
type fakeBlocks struct {
	blocks []sessdomain.Block
	plain  string
	raw    []byte
	// lastGet records what the handler passed down, so a test can prove a parameter
	// survived the trip rather than only that the call succeeded.
	lastGet struct {
		id, sessionID string
		include       sessdomain.Include
	}
	lastFilter sessdomain.BlockFilter
	lastSearch sessdomain.SearchQuery
	nextCursor sessdomain.Cursor
	err        error
}

func (f *fakeBlocks) List(_ context.Context, filter sessdomain.BlockFilter) (sessdomain.BlockPage, error) {
	f.lastFilter = filter
	if f.err != nil {
		return sessdomain.BlockPage{}, f.err
	}
	return sessdomain.BlockPage{Items: f.blocks, NextCursor: f.nextCursor}, nil
}

func (f *fakeBlocks) Get(_ context.Context, id, sessionID string, include sessdomain.Include) (sessdomain.Block, sessdomain.BlockOutput, error) {
	f.lastGet.id, f.lastGet.sessionID, f.lastGet.include = id, sessionID, include
	if f.err != nil {
		return sessdomain.Block{}, sessdomain.BlockOutput{}, f.err
	}
	if len(f.blocks) == 0 {
		return sessdomain.Block{}, sessdomain.BlockOutput{},
			fmt.Errorf("%w: block %s", sessdomain.ErrNotFound, id)
	}
	output := sessdomain.BlockOutput{}
	switch include {
	case sessdomain.IncludePlain:
		output.Plain = f.plain
	case sessdomain.IncludeRaw:
		output.Raw = f.raw
		output.RawTruncated = true
	}
	return f.blocks[0], output, nil
}

func (f *fakeBlocks) Search(_ context.Context, query sessdomain.SearchQuery) (sessdomain.SearchPage, error) {
	f.lastSearch = query
	if f.err != nil {
		return sessdomain.SearchPage{}, f.err
	}
	hits := make([]sessdomain.SearchHit, 0, len(f.blocks))
	for _, block := range f.blocks {
		hits = append(hits, sessdomain.SearchHit{Block: block, Snippet: "…[FAIL] internal/store…"})
	}
	return sessdomain.SearchPage{Items: hits, NextCursor: f.nextCursor}, nil
}

var _ sessports.Blocks = (*fakeBlocks)(nil)

// testServerWithBlocks starts a server wired to a fake history.
func testServerWithBlocks(t *testing.T, blocks sessports.Blocks) *Server {
	t.Helper()

	dir := t.TempDir()
	eventBus := bus.New()
	t.Cleanup(eventBus.Close)

	s, err := Listen(t.Context(), Config{
		SocketPath:    filepath.Join(dir, SocketFileName),
		TokenPath:     filepath.Join(dir, TokenFileName),
		DaemonVersion: "0.1.0-test",
		Blocks:        blocks,
		Bus:           eventBus,
	})
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	served := make(chan error, 1)
	go func() { served <- s.Serve(ctx) }()

	t.Cleanup(func() {
		cancel()
		_ = s.Close()
		select {
		case err := <-served:
			if err != nil {
				t.Errorf("Serve: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("Serve did not return after Close")
		}
	})
	return s
}

// sampleBlock is a finished block with every optional field populated.
func sampleBlock() sessdomain.Block {
	startedAt := time.UnixMilli(1_757_592_001_000).UTC()
	endedAt := startedAt.Add(4200 * time.Millisecond)
	exitCode := 1
	return sessdomain.Block{
		ID: "blk_01J9Z3K8T2QH6W4V5X7Y8Z9A0B", SessionID: "ses_01J9Z3K8T2QH6W4V5X7Y8Z9A0B",
		Origin: sessdomain.OriginUser, Command: "go test ./...", CWD: "/home/u/repo",
		Host: "thinkpad", State: sessdomain.BlockFinished, ExitCode: &exitCode,
		StartedAt: startedAt, EndedAt: &endedAt, OutputBytes: 1834,
	}
}

// resultMap decodes a successful response's result.
func resultMap(t *testing.T, resp response) map[string]any {
	t.Helper()

	if resp.Error != nil {
		t.Fatalf("call failed: %+v", resp.Error)
	}
	encoded, err := json.Marshal(resp.Result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(encoded, &out); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	return out
}

// TestBlockGetLast_REQ_CLI_002 is the wire half of REQ-CLI-002: `umb block last --json`
// prints the last closed block "following the API Block schema", so the schema is what this
// checks. That the reserved id resolves to the right block is tested against real SQL in the
// store and against a real shell in the integration package.
func TestBlockGetLast_REQ_CLI_002(t *testing.T) {
	t.Parallel()

	fake := &fakeBlocks{blocks: []sessdomain.Block{sampleBlock()}, plain: "FAIL\tinternal/store\n"}
	s := testServerWithBlocks(t, fake)
	c := dial(t, s)
	c.hello(s.Token(), ClientCLI)

	resp := c.call(2, "block.get", map[string]any{"block_id": "last", "include": "plain"})
	result := resultMap(t, resp)

	// The reserved id reaches the module unchanged: resolving "last" is its job, not the
	// wire's.
	if fake.lastGet.id != sessdomain.BlockLast {
		t.Errorf("the module was asked for %q, want %q", fake.lastGet.id, sessdomain.BlockLast)
	}
	if fake.lastGet.include != sessdomain.IncludePlain {
		t.Errorf("include reached the module as %q", fake.lastGet.include)
	}

	// Every key of API Spec §4, present even when null, so a client can read the same
	// fields off a query result and off a block.closed notification.
	for _, key := range []string{
		"id", "session_id", "origin", "thread_id", "command", "cwd", "host", "state",
		"exit_code", "started_at", "ended_at", "duration_ms", "output_bytes",
		"output_truncated",
	} {
		if _, ok := result[key]; !ok {
			t.Errorf("block.get result has no %q", key)
		}
	}
	switch {
	case result["state"] != "finished":
		t.Errorf("state is %v", result["state"])
	case result["exit_code"] != float64(1):
		t.Errorf("exit_code is %v, want 1", result["exit_code"])
	case result["duration_ms"] != float64(4200):
		t.Errorf("duration_ms is %v, want 4200", result["duration_ms"])
	case result["thread_id"] != nil:
		t.Errorf("thread_id is %v, want null for a user block", result["thread_id"])
	case result["output_plain"] != "FAIL\tinternal/store\n":
		t.Errorf("output_plain is %v", result["output_plain"])
	case result["output_raw_b64"] != nil:
		t.Errorf("output_raw_b64 is present for include \"plain\": %v", result["output_raw_b64"])
	}
}

func TestBlockGetRawIsBase64AndFlagsTruncation(t *testing.T) {
	t.Parallel()

	fake := &fakeBlocks{blocks: []sessdomain.Block{sampleBlock()}, raw: []byte("\x1b[1;32mPASS\x1b[0m\r\n")}
	s := testServerWithBlocks(t, fake)
	c := dial(t, s)
	c.hello(s.Token(), ClientCLI)

	result := resultMap(t, c.call(2, "block.get", map[string]any{
		"block_id": "blk_01J9Z3K8T2QH6W4V5X7Y8Z9A0B", "include": "raw",
	}))

	encoded, ok := result["output_raw_b64"].(string)
	if !ok {
		t.Fatalf("output_raw_b64 is %v, want a base64 string", result["output_raw_b64"])
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("output_raw_b64 does not decode: %v", err)
	}
	if string(decoded) != "\x1b[1;32mPASS\x1b[0m\r\n" {
		t.Errorf("the raw output did not survive the trip: %q", decoded)
	}
	// A client that is handed part of a block must be told so, or it will believe it has
	// the whole of what the command printed.
	if result["output_raw_truncated"] != true {
		t.Errorf("output_raw_truncated is %v, want true", result["output_raw_truncated"])
	}
	if result["output_plain"] != nil {
		t.Errorf("output_plain is present for include \"raw\": %v", result["output_plain"])
	}
}

func TestBlockListPagesOverTheWire(t *testing.T) {
	t.Parallel()

	fake := &fakeBlocks{
		blocks: []sessdomain.Block{sampleBlock()},
		nextCursor: sessdomain.Cursor{
			StartedAt: time.UnixMilli(1_757_592_001_000).UTC(),
			BlockID:   "blk_01J9Z3K8T2QH6W4V5X7Y8Z9A0B",
		},
	}
	s := testServerWithBlocks(t, fake)
	c := dial(t, s)
	c.hello(s.Token(), ClientCLI)

	result := resultMap(t, c.call(2, "block.list", map[string]any{
		"session_id": "ses_1", "state": "finished", "limit": 10,
	}))

	if fake.lastFilter.SessionID != "ses_1" || fake.lastFilter.State != sessdomain.BlockFinished {
		t.Errorf("the filter reached the module as %+v", fake.lastFilter)
	}
	if fake.lastFilter.Limit != 10 {
		t.Errorf("limit reached the module as %d", fake.lastFilter.Limit)
	}

	items, ok := result["items"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("items is %v", result["items"])
	}
	token, ok := result["next_cursor"].(string)
	if !ok || token == "" {
		t.Fatalf("next_cursor is %v, want an opaque token", result["next_cursor"])
	}

	// The token the client gets back must be the one the module understands. A cursor that
	// only round-trips through the encoder and not through the handler would page from the
	// start forever.
	resultMap(t, c.call(3, "block.list", map[string]any{"cursor": token}))
	if fake.lastFilter.Cursor.BlockID != "blk_01J9Z3K8T2QH6W4V5X7Y8Z9A0B" {
		t.Errorf("the cursor reached the module as %+v", fake.lastFilter.Cursor)
	}
}

func TestBlockListReturnsAnEmptyArrayNotNull(t *testing.T) {
	t.Parallel()

	s := testServerWithBlocks(t, &fakeBlocks{})
	c := dial(t, s)
	c.hello(s.Token(), ClientCLI)

	result := resultMap(t, c.call(2, "block.list", map[string]any{}))
	items, ok := result["items"].([]any)
	if !ok {
		t.Fatalf("items is %v, want [] rather than null", result["items"])
	}
	if len(items) != 0 {
		t.Errorf("items has %d entries", len(items))
	}
	// The key is present and null on the last page. A client looping until it disappears
	// would never stop.
	if value, present := result["next_cursor"]; !present || value != nil {
		t.Errorf("next_cursor is %v (present %v), want an explicit null", value, present)
	}
}

func TestBlockSearchReturnsHitsWithSnippets(t *testing.T) {
	t.Parallel()

	fake := &fakeBlocks{blocks: []sessdomain.Block{sampleBlock()}}
	s := testServerWithBlocks(t, fake)
	c := dial(t, s)
	c.hello(s.Token(), ClientCLI)

	result := resultMap(t, c.call(2, "block.search", map[string]any{"query": "FAIL", "limit": 5}))

	if fake.lastSearch.Query != "FAIL" || fake.lastSearch.Limit != 5 {
		t.Errorf("the query reached the module as %+v", fake.lastSearch)
	}
	items, ok := result["items"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("items is %v", result["items"])
	}
	hit, ok := items[0].(map[string]any)
	if !ok {
		t.Fatalf("a hit is %v", items[0])
	}
	if _, present := hit["block"]; !present {
		t.Error("a hit has no block")
	}
	if hit["snippet"] == "" || hit["snippet"] == nil {
		t.Errorf("a hit has no snippet: %v", hit)
	}
}

func TestBlockMethodsRejectABadCursor(t *testing.T) {
	t.Parallel()

	s := testServerWithBlocks(t, &fakeBlocks{})
	c := dial(t, s)
	c.hello(s.Token(), ClientCLI)

	for i, method := range []string{"block.list", "block.search"} {
		params := map[string]any{"cursor": "not-a-cursor!!"}
		if method == "block.search" {
			params["query"] = "x"
		}
		resp := c.call(2+i, method, params)
		if resp.Error == nil {
			t.Fatalf("%s accepted a malformed cursor", method)
		}
		if resp.Error.Code != codeValidationError {
			t.Errorf("%s answered code %d, want VALIDATION_ERROR", method, resp.Error.Code)
		}
	}
}

// TestBlockMethodsAreAllowedForTheCLI covers the allowlist of API Spec §2: `umb` may read
// blocks even though it may not drive a terminal.
func TestBlockMethodsAreAllowedForTheCLI(t *testing.T) {
	t.Parallel()

	s := testServerWithBlocks(t, &fakeBlocks{blocks: []sessdomain.Block{sampleBlock()}})
	c := dial(t, s)
	c.hello(s.Token(), ClientCLI)

	if resp := c.call(2, "block.list", map[string]any{}); resp.Error != nil {
		t.Errorf("block.list refused the cli client: %+v", resp.Error)
	}
	// The contrast: session.* is not in the cli allowlist.
	resp := c.call(3, "session.list", map[string]any{})
	if resp.Error == nil || resp.Error.Code != codeMethodNotFound {
		t.Errorf("session.list answered %+v, want METHOD_NOT_FOUND for the cli client", resp.Error)
	}
}

// TestBlockMethodsAreAdvertised checks that `block` appears in the handshake's capabilities,
// which is how a client learns the namespace exists.
func TestBlockMethodsAreAdvertised(t *testing.T) {
	t.Parallel()

	s := testServerWithBlocks(t, &fakeBlocks{})
	c := dial(t, s)
	resp := c.hello(s.Token(), ClientCLI)

	result := resultMap(t, resp)
	capabilities, ok := result["capabilities"].([]any)
	if !ok {
		t.Fatalf("capabilities is %v", result["capabilities"])
	}
	found := false
	for _, capability := range capabilities {
		// API Spec §2 calls the namespace `blocks`; `block.*` is the method prefix.
		if capability == "blocks" {
			found = true
		}
	}
	if !found {
		t.Errorf("capabilities is %v, want it to advertise blocks", capabilities)
	}
}
