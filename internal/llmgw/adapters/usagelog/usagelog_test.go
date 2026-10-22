package usagelog

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/ecrespo/umbral/internal/llmgw/domain"
	"github.com/ecrespo/umbral/internal/store"
)

// TestUsageRowsAreWritten_REQ_LLM_005: a record becomes one `usage` row with its tokens, time
// to first token and cost; a call for no thread, or one whose first token never came, stores
// NULL; a status the table does not know is refused rather than stored.
func TestUsageRowsAreWritten_REQ_LLM_005(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	st, err := store.Open(ctx, store.Options{Path: filepath.Join(t.TempDir(), "umbral.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	if _, err := st.DB().ExecContext(ctx, `INSERT INTO threads (id, cwd, created_at, updated_at) VALUES ('thr_1', '/', 1, 1)`); err != nil {
		t.Fatal(err)
	}
	l, err := New(st)
	if err != nil {
		t.Fatal(err)
	}
	ms := int64(840)
	if err := l.Record(ctx, domain.UsageRecord{
		ThreadID: "thr_1", TurnID: "tu_1", ModelID: "or/kimi", Provider: "or", Status: domain.UsageOK,
		InTokens: 1500, OutTokens: 500, FirstTokenMS: &ms, CostMicroUSD: 2005, CreatedAt: 9,
	}); err != nil {
		t.Fatal(err)
	}
	if err := l.Record(ctx, domain.UsageRecord{
		ModelID: "or/kimi", Provider: "or", Status: domain.UsageRateLimited, Error: "HTTP 429", CreatedAt: 10,
	}); err != nil {
		t.Fatal(err)
	}

	type row struct {
		thread, turn, errText *string
		model, provider, stat string
		in, out, cost, at     int64
		first                 *int64
	}
	rows, err := st.DB().QueryContext(ctx, `SELECT thread_id, turn_id, model_id, provider, status,
		in_tokens, out_tokens, first_token_ms, cost_micro_usd, error, created_at FROM usage ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var got []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.thread, &r.turn, &r.model, &r.provider, &r.stat, &r.in, &r.out, &r.first, &r.cost, &r.errText, &r.at); err != nil {
			t.Fatal(err)
		}
		got = append(got, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("rows = %d, want 2", len(got))
	}
	ok := got[0]
	if ok.thread == nil || *ok.thread != "thr_1" || ok.turn == nil || *ok.turn != "tu_1" || ok.model != "or/kimi" ||
		ok.provider != "or" || ok.stat != "ok" || ok.in != 1500 || ok.out != 500 || ok.first == nil || *ok.first != 840 ||
		ok.cost != 2005 || ok.errText != nil || ok.at != 9 {
		t.Errorf("ok row = %+v", ok)
	}
	failed := got[1]
	if failed.thread != nil || failed.turn != nil || failed.first != nil || failed.stat != "rate_limited" ||
		failed.errText == nil || *failed.errText != "HTTP 429" {
		t.Errorf("failed row = %+v", failed)
	}

	if err := l.Record(ctx, domain.UsageRecord{ModelID: "m", Provider: "p", Status: "bogus"}); err == nil {
		t.Error("a status the table refuses was stored")
	}
	if _, err := New(nil); err == nil {
		t.Error("New(nil) accepted")
	}
}
