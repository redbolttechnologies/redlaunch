package main

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
)

func TestMCPStartsWithoutWebApplicationConfiguration(t *testing.T) {
	for _, key := range []string{"REDLAUNCH_URL", "REDLAUNCH_RUN_TOKEN", "GOOGLE_CLIENT_ID", "GOOGLE_CLIENT_SECRET", "AUTH_SESSION_SECRET"} {
		t.Setenv(key, "")
	}
	t.Setenv("DB_PATH", t.TempDir()+"/unused.db")
	var output bytes.Buffer
	input := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}` + "\n"
	if err := runMCP(context.Background(), nil, strings.NewReader(input), &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), `"name":"redlaunch"`) {
		t.Fatal("MCP initialization failed")
	}
	for _, args := range [][]string{{"extra"}, {"--unknown"}, {"--allow-run"}} {
		if err := runMCP(context.Background(), args, strings.NewReader(""), io.Discard); err == nil {
			t.Fatal("invalid MCP configuration accepted")
		}
	}
}
