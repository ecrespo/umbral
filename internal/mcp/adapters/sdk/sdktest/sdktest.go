// Package sdktest is an MCP server for tests, written with the official SDK: the same
// protocol a real server speaks, over stdio as a child process or over streamable HTTP.
package sdktest

import (
	"context"
	"net/http"
	"os"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ChildEnv is the variable that makes a test binary serve MCP on stdio instead of running its
// tests: Serve, called from TestMain, checks it.
const ChildEnv = "UMBRAL_SDKTEST_MCP_SERVER"

type echoIn struct {
	Text string `json:"text" jsonschema:"what to echo"`
}

type envIn struct {
	Name string `json:"name" jsonschema:"the variable to read"`
}

type sleepIn struct {
	Millis int `json:"millis" jsonschema:"how long to sleep"`
}

type empty struct{}

func textResult(s string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}}
}

// NewServer is the test server. Its tools: `echo` returns its text; `env` reads one of the
// process's environment variables; `fail` reports a failed call (`isError`); `sleep` waits;
// and `exit` ends the process, as a crash would.
func NewServer() *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "sdktest", Version: "1"}, nil)
	mcp.AddTool(s, &mcp.Tool{Name: "echo", Description: "Echo the text."},
		func(_ context.Context, _ *mcp.CallToolRequest, in echoIn) (*mcp.CallToolResult, any, error) {
			return textResult(in.Text), nil, nil
		})
	mcp.AddTool(s, &mcp.Tool{Name: "env", Description: "Read an environment variable."},
		func(_ context.Context, _ *mcp.CallToolRequest, in envIn) (*mcp.CallToolResult, any, error) {
			return textResult(os.Getenv(in.Name)), nil, nil
		})
	mcp.AddTool(s, &mcp.Tool{Name: "fail", Description: "Fail."},
		func(context.Context, *mcp.CallToolRequest, empty) (*mcp.CallToolResult, any, error) {
			r := textResult("it failed")
			r.IsError = true
			return r, nil, nil
		})
	mcp.AddTool(s, &mcp.Tool{Name: "sleep", Description: "Sleep."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in sleepIn) (*mcp.CallToolResult, any, error) {
			select {
			case <-time.After(time.Duration(in.Millis) * time.Millisecond):
			case <-ctx.Done():
			}
			return textResult("slept"), nil, nil
		})
	mcp.AddTool(s, &mcp.Tool{Name: "exit", Description: "End the process."},
		func(context.Context, *mcp.CallToolRequest, empty) (*mcp.CallToolResult, any, error) {
			os.Exit(3)
			return nil, nil, nil
		})
	return s
}

// Serve serves the test server on stdio and exits, when the process was started as one
// (ChildEnv is set); otherwise it returns at once. A TestMain calls it first.
func Serve() {
	if os.Getenv(ChildEnv) == "" {
		return
	}
	_ = NewServer().Run(context.Background(), &mcp.StdioTransport{})
	os.Exit(0)
}

// Handler serves the test server over streamable HTTP.
func Handler() http.Handler {
	s := NewServer()
	return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s }, nil)
}
