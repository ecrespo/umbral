package egresslog

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ecrespo/umbral/internal/llmgw/domain"
	"github.com/ecrespo/umbral/internal/store"
)

// TestEgressRowsAreWritten_REQ_SEC_002: a record becomes one `egress_log` row with the
// thread, or NULL for the daemon's own requests; a hash of the wrong length is refused by
// the table, so a malformed record fails rather than being stored.
func TestEgressRowsAreWritten_REQ_SEC_002(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	st, err := store.Open(ctx, store.Options{Path: filepath.Join(t.TempDir(), "umbral.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	l, err := New(st)
	if err != nil {
		t.Fatal(err)
	}
	hash := strings.Repeat("ab", 32)
	if err := l.Record(ctx, domain.EgressRecord{Provider: "or", Host: "openrouter.ai", Bytes: 0, PayloadSHA256: hash, CreatedAt: 7}); err != nil {
		t.Fatal(err)
	}
	var thread *string
	var provider, host, sum string
	var n, at int64
	if err := st.DB().QueryRowContext(ctx, `SELECT thread_id, provider, host, bytes, payload_sha256, created_at FROM egress_log`).
		Scan(&thread, &provider, &host, &n, &sum, &at); err != nil {
		t.Fatal(err)
	}
	if thread != nil || provider != "or" || host != "openrouter.ai" || n != 0 || sum != hash || at != 7 {
		t.Errorf("row = %v %s %s %d %s %d", thread, provider, host, n, sum, at)
	}
	if err := l.Record(ctx, domain.EgressRecord{Provider: "or", Host: "h", PayloadSHA256: "short"}); err == nil {
		t.Error("a malformed hash was stored")
	}
	if _, err := New(nil); err == nil {
		t.Error("New(nil) accepted")
	}
}
