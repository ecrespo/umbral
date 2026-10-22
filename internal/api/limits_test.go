package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	sessdomain "github.com/ecrespo/umbral/internal/sessions/domain"
)

// oneMiB is the smallest frame limit a daemon may be configured with, which is what these
// tests run under so that "over the limit" costs a couple of megabytes, not tens.
const oneMiB = 1 << 20

// secretMarker fills every oversized payload in this file, so a log line that leaked content
// is found by looking for it (Art. 7).
const secretMarker = "s3cr3t-"

// addTestMethod registers a method that answers with a string of the given size. It is called
// before any connection is dialed, so the daemon reads the table only after this write.
func addTestMethod(s *Server, name string, size int) {
	payload := strings.Repeat(secretMarker, size/len(secretMarker)+1)[:size]
	s.methods[name] = method{
		handle: func(context.Context, *conn, json.RawMessage) (any, error) { return payload, nil },
		params: emptyResult{}, result: "",
	}
}

// syncBuffer is a log destination the daemon's goroutines and the test can share.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// errorSizes reads RESULT_TOO_LARGE's data as numbers, which is the form the delta requires
// so a client can act on them without parsing a message.
func errorSizes(t *testing.T, resp response) (size, limit float64) {
	t.Helper()
	raw, err := json.Marshal(resp.Error.Data)
	if err != nil {
		t.Fatal(err)
	}
	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatal(err)
	}
	size, okSize := data["size_bytes"].(float64)
	limit, okLimit := data["limit_bytes"].(float64)
	if !okSize || !okLimit {
		t.Fatalf("RESULT_TOO_LARGE data = %s, want numeric size_bytes and limit_bytes", raw)
	}
	return size, limit
}

// TestAResponseOverTheLimitIsResultTooLarge_REQ_API_005: the daemon never writes a frame
// its peer would have to refuse. A response over the connection's limit becomes
// RESULT_TOO_LARGE and the connection carries on; a notification over it becomes
// `limits.notification_dropped` under the same `seq`; and `block.get` shortens its output to
// fit instead, saying how much it left out of this response.
func TestAResponseOverTheLimitIsResultTooLarge_REQ_API_005(t *testing.T) {
	t.Parallel()

	stored := bytes.Repeat([]byte("\x1b[1mraw\x1b[0m\n"), 3*oneMiB/12)
	// An escape-heavy head, then two-byte runes where the cut lands: the measure must be of
	// the JSON, not of the Go string, and the cut must fall on a rune boundary. The plain
	// subtest shifts the text by one byte too, so one of the two parities would cut mid-rune.
	plain := strings.Repeat("<\"\\>\t", 20<<10) + strings.Repeat("é", 400<<10)
	blocks := &fakeBlocks{
		blocks: []sessdomain.Block{{ID: "blk_1", SessionID: "ses_1", State: sessdomain.BlockFinished}},
		raw:    stored, plain: plain,
	}
	s := testServerWithConfig(t, Config{MaxMessageBytes: oneMiB, Blocks: blocks})
	addTestMethod(s, "test.big", 2*oneMiB)
	c := dial(t, s)

	hello := c.hello(s.Token(), ClientTUI)
	var greeted helloResult
	decodeResult(t, hello, &greeted)
	if greeted.MaxMessageBytes != oneMiB {
		t.Errorf("system.hello announced max_message_bytes = %d, want %d", greeted.MaxMessageBytes, oneMiB)
	}

	t.Run("a response becomes RESULT_TOO_LARGE and the connection stays", func(t *testing.T) {
		resp := c.call(7, "test.big", nil)
		if resp.Error == nil || resp.Error.Code != codeResultTooLarge {
			t.Fatalf("a 2 MiB result = %+v, want RESULT_TOO_LARGE (%d)", resp.Error, codeResultTooLarge)
		}
		if string(resp.ID) != "7" {
			t.Errorf("id = %s, want the request's 7", resp.ID)
		}
		size, limit := errorSizes(t, resp)
		if size <= 2*oneMiB || limit != oneMiB {
			t.Errorf("size_bytes = %v, limit_bytes = %v; want over %d and %d", size, limit, 2*oneMiB, oneMiB)
		}
		if next := c.call(8, "system.status", nil); next.Error != nil {
			t.Errorf("the call after a refused result failed: %+v", next.Error)
		}
	})

	t.Run("a notification becomes limits.notification_dropped under its seq", func(t *testing.T) {
		// On a goroutine of its own, like the daemon's dispatcher: were the notification written
		// whole, nobody would be reading the socket yet and the write would block this test.
		seq := s.nextSeq()
		go s.broadcast("test.big_event", strings.Repeat(secretMarker, 2*oneMiB/len(secretMarker)), seq)

		method, got, params := c.readNotification(t)
		if method != "limits.notification_dropped" || got != seq {
			t.Fatalf("got %s seq %d, want limits.notification_dropped seq %d", method, got, seq)
		}
		var dropped struct {
			Method     string `json:"method"`
			SizeBytes  *int64 `json:"size_bytes"`
			LimitBytes *int64 `json:"limit_bytes"`
		}
		if err := json.Unmarshal(params, &dropped); err != nil {
			t.Fatal(err)
		}
		if dropped.Method != "test.big_event" || dropped.SizeBytes == nil || *dropped.SizeBytes <= 2*oneMiB-16 ||
			dropped.LimitBytes == nil || *dropped.LimitBytes != oneMiB {
			t.Errorf("params = %s", params)
		}
	})

	t.Run("block.get raw is shortened on a 3-byte boundary", func(t *testing.T) {
		resp := c.call(9, "block.get", map[string]any{"block_id": "blk_1", "include": "raw"})
		if resp.Error != nil {
			t.Fatalf("block.get raw = %+v", resp.Error)
		}
		assertFits(t, resp)
		var got blockResult
		decodeResult(t, resp, &got)
		if got.OutputRawB64 == nil || got.OutputResponseTruncatedBytes == nil {
			t.Fatalf("output_raw_b64 or output_response_truncated_bytes missing: %+v", got.Block)
		}
		decoded, err := base64.StdEncoding.DecodeString(*got.OutputRawB64)
		if err != nil {
			t.Fatal(err)
		}
		if len(decoded)%3 != 0 || !bytes.Equal(decoded, stored[:len(decoded)]) {
			t.Errorf("the raw output is not a 3-byte-aligned prefix of the stored bytes (%d)", len(decoded))
		}
		if int64(len(decoded))+*got.OutputResponseTruncatedBytes != int64(len(stored)) {
			t.Errorf("returned %d + omitted %d != stored %d", len(decoded),
				*got.OutputResponseTruncatedBytes, len(stored))
		}
		if got.OutputTruncated {
			t.Error("output_truncated changed meaning: it reports the stored capture, not this response")
		}
	})

	for shift := range 2 {
		t.Run(fmt.Sprintf("block.get plain is shortened on a UTF-8 boundary (shift %d)", shift), func(t *testing.T) {
			text := strings.Repeat("a", shift) + plain
			blocks.plain = text
			resp := c.call(10, "block.get", map[string]any{"block_id": "blk_1", "include": "plain"})
			if resp.Error != nil {
				t.Fatalf("block.get plain = %+v", resp.Error)
			}
			assertFits(t, resp)
			var got blockResult
			decodeResult(t, resp, &got)
			if got.OutputPlain == nil || got.OutputResponseTruncatedBytes == nil {
				t.Fatal("output_plain or output_response_truncated_bytes missing")
			}
			if !utf8.ValidString(*got.OutputPlain) || !strings.HasPrefix(text, *got.OutputPlain) {
				t.Error("the plain output is not a valid UTF-8 prefix of the stored transcript")
			}
			if int64(len(*got.OutputPlain))+*got.OutputResponseTruncatedBytes != int64(len(text)) {
				t.Errorf("returned %d + omitted %d != stored %d", len(*got.OutputPlain),
					*got.OutputResponseTruncatedBytes, len(text))
			}
		})
	}

	t.Run("block.get that fits is untouched", func(t *testing.T) {
		blocks.raw = stored[:1000]
		defer func() { blocks.raw = stored }()
		resp := c.call(11, "block.get", map[string]any{"block_id": "blk_1", "include": "raw"})
		var got blockResult
		decodeResult(t, resp, &got)
		if got.OutputResponseTruncatedBytes != nil {
			t.Errorf("output_response_truncated_bytes = %d on a result that fit",
				*got.OutputResponseTruncatedBytes)
		}
	})
}

// assertFits re-encodes a response the way the daemon wrote it and checks the frame limit.
func assertFits(t *testing.T, resp response) {
	t.Helper()
	raw, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw)+1 > oneMiB {
		t.Errorf("the response is %d bytes, over the %d limit", len(raw)+1, oneMiB)
	}
}

// TestFramesAreCounted_REQ_OBS_005: how close traffic comes to the limit is visible before
// anything fails. Every counter is driven, read back through both `system.status` and
// `limits.get`, and the warn lines are checked to carry the method and the size and never
// the payload.
func TestFramesAreCounted_REQ_OBS_005(t *testing.T) {
	t.Parallel()

	logs := &syncBuffer{}
	s := testServerWithConfig(t, Config{
		MaxMessageBytes: oneMiB,
		Logger:          slog.New(slog.NewJSONHandler(logs, nil)),
	})
	addTestMethod(s, "test.near", 900<<10) // above 75 % of 1 MiB
	addTestMethod(s, "test.big", 2*oneMiB)

	c := dial(t, s)
	if resp := c.hello(s.Token(), ClientTUI); resp.Error != nil {
		t.Fatalf("handshake: %+v", resp.Error)
	}

	// In: one padded request of a known size.
	line := paddedCall(t, 2, "system.status", 600<<10)
	c.send(line)
	if resp := c.read(); resp.Error != nil {
		t.Fatalf("padded status: %+v", resp.Error)
	}
	// Out: one near the limit, one over it, and one dropped notification.
	if resp := c.call(3, "test.near", nil); resp.Error != nil {
		t.Fatalf("test.near: %+v", resp.Error)
	}
	if resp := c.call(4, "test.big", nil); resp.Error == nil || resp.Error.Code != codeResultTooLarge {
		t.Fatalf("test.big = %+v, want RESULT_TOO_LARGE", resp.Error)
	}
	go s.broadcast("test.big_event", strings.Repeat(secretMarker, 2*oneMiB/len(secretMarker)), s.nextSeq())
	if method, _, _ := c.readNotification(t); method != "limits.notification_dropped" {
		t.Fatalf("got %s, want limits.notification_dropped", method)
	}

	// Refused in: a second connection sends a line past its limit and is closed.
	intruder := dial(t, s)
	if resp := intruder.hello(s.Token(), ClientTUI); resp.Error != nil {
		t.Fatalf("handshake: %+v", resp.Error)
	}
	intruder.sendPastTheLimit(paddedCall(t, 2, "system.status", 2*oneMiB))
	if resp := intruder.read(); resp.Error == nil || resp.Error.Code != codeValidationError {
		t.Fatalf("an oversized line = %+v, want VALIDATION_ERROR", resp.Error)
	}
	intruder.expectClosed()

	want := Frames{
		LimitBytes:     oneMiB,
		LargestInBytes: int64(len(line)),
		RefusedIn:      1,
		RefusedOut:     2,
		NearLimitOut:   1,
	}
	check := func(source string, got Frames) {
		t.Helper()
		if got.LargestOutBytes < 900<<10 || got.LargestOutBytes > oneMiB {
			t.Errorf("%s: largest_out_bytes = %d, want the near-limit answer (%d..%d)",
				source, got.LargestOutBytes, 900<<10, oneMiB)
		}
		got.LargestOutBytes = 0
		if got != want {
			t.Errorf("%s: frames = %+v, want %+v", source, got, want)
		}
	}

	var status StatusResult
	decodeResult(t, c.call(5, "system.status", nil), &status)
	check("system.status", status.Frames)

	var limits limitsResult
	decodeResult(t, c.call(6, "limits.get", nil), &limits)
	check("limits.get", limits.Frames)
	if limits.ConfiguredMaxMessageBytes != oneMiB {
		t.Errorf("configured_max_message_bytes = %d, want %d", limits.ConfiguredMaxMessageBytes, oneMiB)
	}

	// The warn lines: every refusal and the first near-limit frame, with method and size.
	deadline := time.Now().Add(2 * time.Second)
	for strings.Count(logs.String(), `"level":"WARN"`) < 4 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	text := logs.String()
	for _, needle := range []string{`"method":"test.big"`, `"method":"test.big_event"`, `"method":"test.near"`} {
		if !strings.Contains(text, needle) {
			t.Errorf("no warn line names %s:\n%s", needle, text)
		}
	}
	if n := strings.Count(text, `"level":"WARN"`); n != 4 {
		t.Errorf("%d warn lines, want 4 (two refused out, one refused in, the first near limit):\n%s", n, text)
	}
	// The inbound refusal has no method to name — the line was never parsed — so it names
	// the size it knows: more than the limit.
	if !strings.Contains(text, `"size_bytes_at_least":1048577`) {
		t.Errorf("the inbound refusal's warn line does not carry its size:\n%s", text)
	}
	if strings.Contains(text, secretMarker) {
		t.Error("a warn line carries payload bytes; Art. 7 allows the method and the size only")
	}
}
