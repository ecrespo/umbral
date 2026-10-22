package api

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"sync/atomic"
)

// methodNotificationDropped replaces a notification that would not fit its connection's
// frame limit (REQ-API-005, API Spec §6). It lives in the `limits` namespace so it does not
// share a prefix with a method a client calls.
const methodNotificationDropped = "limits.notification_dropped"

// nearLimitPercent is where a written frame counts as close to its limit (REQ-OBS-005).
const nearLimitPercent = 75

// Frames is how close traffic has come to the frame limit this daemon run (REQ-OBS-005,
// API Spec §5.2). It is reported by `system.status` and `limits.get`, and it is per run: a
// restart starts every counter again, as `seq` does.
type Frames struct {
	// LimitBytes is the limit a new connection gets once it completes the handshake.
	LimitBytes int64 `json:"limit_bytes"`
	// LargestInBytes and LargestOutBytes are the largest frames read and written, the `\n`
	// counted, as the limit counts it.
	LargestInBytes  int64 `json:"largest_in_bytes"`
	LargestOutBytes int64 `json:"largest_out_bytes"`
	// RefusedIn counts inbound frames over the limit, which close their connection.
	RefusedIn int64 `json:"refused_in"`
	// RefusedOut counts responses answered RESULT_TOO_LARGE and notifications replaced by
	// limits.notification_dropped.
	RefusedOut int64 `json:"refused_out"`
	// NearLimitOut counts frames written above 75 % of their connection's limit.
	NearLimitOut int64 `json:"near_limit_out"`
}

// frameStats is Frames as the connections update it: lock-free, because every write on
// every connection goes through it.
type frameStats struct {
	largestIn, largestOut          atomic.Int64
	refusedIn, refusedOut, nearOut atomic.Int64
	nearLogged                     atomic.Bool
}

func (f *frameStats) snapshot(limit int64) Frames {
	return Frames{
		LimitBytes:      limit,
		LargestInBytes:  f.largestIn.Load(),
		LargestOutBytes: f.largestOut.Load(),
		RefusedIn:       f.refusedIn.Load(),
		RefusedOut:      f.refusedOut.Load(),
		NearLimitOut:    f.nearOut.Load(),
	}
}

// raise stores n in max if it is larger than what max holds.
func raise(max *atomic.Int64, n int64) {
	for {
		current := max.Load()
		if n <= current || max.CompareAndSwap(current, n) {
			return
		}
	}
}

// resultTooLarge is RESULT_TOO_LARGE with the two numbers a client acts on.
func resultTooLarge(size, limit int64) *wireError {
	return &wireError{
		Code: codeResultTooLarge,
		Message: fmt.Sprintf("the result is %d bytes, over this connection's %d byte frame limit",
			size, limit),
		Data: &errorData{DomainCode: domainResultTooLarge, SizeBytes: &size, LimitBytes: &limit},
	}
}

// notificationDroppedPayload is limits.notification_dropped's params.
type notificationDroppedPayload struct {
	Method     string `json:"method"`
	SizeBytes  int64  `json:"size_bytes"`
	LimitBytes int64  `json:"limit_bytes"`
}

// frame serialises one outgoing message and holds it to the connection's limit
// (REQ-API-005): a response that does not fit becomes RESULT_TOO_LARGE under its id, a
// notification that does not fit becomes limits.notification_dropped under its seq. Either
// replacement is a few dozen bytes, so it cannot itself exceed the 1 MiB floor.
//
// It returns the bytes to write, `\n` included. Every refusal and the run's first frame above
// 75 % are logged at warn with the method and the size, never the content (Art. 7).
func (c *conn) frame(msg any) ([]byte, error) {
	body, err := json.Marshal(msg)
	if err != nil {
		return nil, err
	}
	limit := c.limit.Load()
	stats := &c.server.frames

	if size := int64(len(body)) + 1; size > limit {
		var replacement any
		var method string
		switch m := msg.(type) {
		case response:
			method = m.method
			replacement = response{JSONRPC: jsonrpcVersion, ID: m.ID, Error: resultTooLarge(size, limit)}
		case notification:
			method = m.Method
			replacement = notification{
				JSONRPC: jsonrpcVersion, Method: methodNotificationDropped, Seq: m.Seq,
				Params: notificationDroppedPayload{Method: m.Method, SizeBytes: size, LimitBytes: limit},
			}
		default:
			return nil, fmt.Errorf("a %T of %d bytes is over the %d byte frame limit", msg, size, limit)
		}
		stats.refusedOut.Add(1)
		c.logger.Warn("an outgoing frame was over the limit and was not written",
			slog.String("method", method), slog.Int64("size_bytes", size),
			slog.Int64("limit_bytes", limit), slog.String("connection_id", c.connectionID))
		if body, err = json.Marshal(replacement); err != nil {
			return nil, err
		}
	}

	size := int64(len(body)) + 1
	raise(&stats.largestOut, size)
	if size*100 > limit*nearLimitPercent {
		stats.nearOut.Add(1)
		if stats.nearLogged.CompareAndSwap(false, true) {
			c.logger.Warn("an outgoing frame came within 25 % of the frame limit",
				slog.String("method", methodOf(msg)), slog.Int64("size_bytes", size),
				slog.Int64("limit_bytes", limit))
		}
	}
	return append(body, '\n'), nil
}

// methodOf names what a message answers or announces, for a log line.
func methodOf(msg any) string {
	switch m := msg.(type) {
	case response:
		return m.method
	case notification:
		return m.Method
	default:
		return ""
	}
}
