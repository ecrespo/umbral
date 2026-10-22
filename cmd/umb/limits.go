package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"math/bits"

	"github.com/ecrespo/umbral/internal/client"
	"github.com/ecrespo/umbral/internal/config"
)

// framesResult mirrors the `frames` object of API Spec §5.2 (REQ-OBS-005).
type framesResult struct {
	LimitBytes      int64 `json:"limit_bytes"`
	LargestInBytes  int64 `json:"largest_in_bytes"`
	LargestOutBytes int64 `json:"largest_out_bytes"`
	RefusedIn       int64 `json:"refused_in"`
	RefusedOut      int64 `json:"refused_out"`
	NearLimitOut    int64 `json:"near_limit_out"`
}

// line is the one `umb status` and `umb limits` print.
func (f framesResult) line() string {
	return fmt.Sprintf("frames: limit %s · largest in %s, out %s · refused %d in, %d out · near limit %d",
		formatSize(f.LimitBytes), formatSize(f.LargestInBytes), formatSize(f.LargestOutBytes),
		f.RefusedIn, f.RefusedOut, f.NearLimitOut)
}

type limitsResult struct {
	Frames                    framesResult `json:"frames"`
	ConfiguredMaxMessageBytes int64        `json:"configured_max_message_bytes"`
}

// cmdLimits is `umb limits` and `umb limits set --max-message <size>` (REQ-OBS-005,
// REQ-CLI-007). The daemon validates and persists; `umb` only reads the size the user typed.
func cmdLimits(ctx context.Context, args []string, stdout, stderr *printer) int {
	if len(args) > 0 && args[0] == "set" {
		return cmdLimitsSet(ctx, args[1:], stdout, stderr)
	}

	fs := flag.NewFlagSet("umb limits", flag.ContinueOnError)
	fs.SetOutput(stderr.w)
	var f commonFlags
	f.register(fs)
	if err := fs.Parse(args); err != nil {
		return exitFailure
	}
	if fs.NArg() > 0 {
		stderr.printf("umb limits: unknown subcommand %q; want `umb limits` or `umb limits set`\n", fs.Arg(0))
		return exitFailure
	}

	c, code := connect(ctx, f.options(), stderr)
	if code != exitOK {
		return code
	}
	defer func() { _ = c.Close() }()

	callCtx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()

	var result limitsResult
	if err := c.Call(callCtx, "limits.get", nil, &result); err != nil {
		return callFailed(stderr, "", err)
	}
	if f.asJSON {
		return printJSON(stdout, stderr, result)
	}
	stdout.printf("frame limit: %s for new connections\n", formatSize(result.ConfiguredMaxMessageBytes))
	stdout.println(result.Frames.line())
	return exitOK
}

func cmdLimitsSet(ctx context.Context, args []string, stdout, stderr *printer) int {
	fs := flag.NewFlagSet("umb limits set", flag.ContinueOnError)
	fs.SetOutput(stderr.w)
	var f commonFlags
	f.register(fs)
	size := fs.String("max-message", "", "the frame limit: bytes, or with KiB or MiB (1MiB to 64MiB)")
	if err := fs.Parse(args); err != nil {
		return exitFailure
	}
	if *size == "" {
		stderr.println("umb limits set: --max-message is required, e.g. --max-message 8MiB")
		return exitFailure
	}
	n, err := config.ParseSize(*size)
	if err != nil {
		stderr.printf("umb limits set: %v\n", err)
		return exitFailure
	}

	c, code := connect(ctx, f.options(), stderr)
	if code != exitOK {
		return code
	}
	defer func() { _ = c.Close() }()

	callCtx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()

	var result struct {
		MaxMessageBytes int64  `json:"max_message_bytes"`
		AppliesTo       string `json:"applies_to"`
	}
	if err := c.Call(callCtx, "limits.set", map[string]any{"max_message_bytes": n}, &result); err != nil {
		return callFailed(stderr, "", err)
	}
	if f.asJSON {
		return printJSON(stdout, stderr, result)
	}
	stdout.printf("frame limit set to %s; it applies to new connections\n", formatSize(result.MaxMessageBytes))
	return exitOK
}

// callFailed reports a failed call and returns the exit code for it. RESULT_TOO_LARGE gets
// the hint REQ-CLI-007 requires, whichever command hit it: the size, the limit and the
// command that raises it. Every other error is printed as it was, after prefix, with its
// domain code when the message does not already carry it.
func callFailed(stderr *printer, prefix string, err error) int {
	var rpcErr *client.Error
	if errors.As(err, &rpcErr) && rpcErr.DomainCode == "RESULT_TOO_LARGE" && rpcErr.LimitBytes > 0 {
		stderr.printf("umb: the answer is larger than the %s frame limit (%s).\n",
			formatSize(rpcErr.LimitBytes), formatSize(rpcErr.SizeBytes))
		if suggested := suggestLimit(rpcErr.SizeBytes); suggested > 0 {
			stderr.printf("     Raise it with: umb limits set --max-message %dMiB\n", suggested>>20)
		} else {
			stderr.printf("     That is past the largest limit the daemon accepts (%s).\n",
				formatSize(config.MaxMaxMessageBytes))
		}
		return exitFailure
	}
	stderr.printf("umb: %s%s\n", prefix, describeError(err))
	return exitFailure
}

// suggestLimit is the smallest power-of-two MiB that holds size, or 0 past the ceiling. A
// power of two because a user who raises the limit once should not have to do it again for
// the next answer that is a little larger.
func suggestLimit(size int64) int64 {
	if size <= 0 || size > config.MaxMaxMessageBytes {
		return 0
	}
	mib := (size + 1<<20 - 1) >> 20
	suggested := int64(1) << bits.Len64(uint64(mib-1)) << 20
	return max(suggested, config.MinMaxMessageBytes)
}

// formatSize renders bytes the way the delta prints them: "512 B", "12 KiB", "4.0 MiB".
func formatSize(n int64) string {
	switch {
	case n < 1<<10:
		return fmt.Sprintf("%d B", n)
	case n < 1<<20:
		return fmt.Sprintf("%.0f KiB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	}
}
