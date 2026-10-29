package shim

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"actionbox/internal/definitions"
	"actionbox/internal/protocol"
)

type fakeBroker struct {
	socketPath string
	requests   chan protocol.Request
}

func startBroker(t *testing.T, resp protocol.Response) *fakeBroker {
	t.Helper()
	dir, err := os.MkdirTemp("", "abs")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	b := &fakeBroker{socketPath: filepath.Join(dir, "b.sock"), requests: make(chan protocol.Request, 10)}
	ln, err := net.Listen("unix", b.socketPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				line, err := bufio.NewReader(conn).ReadBytes('\n')
				if err != nil {
					return
				}
				var req protocol.Request
				if err := json.Unmarshal(line, &req); err != nil {
					return
				}
				b.requests <- req
				out, _ := json.Marshal(resp)
				conn.Write(append(out, '\n'))
			}()
		}
	}()
	return b
}

func (b *fakeBroker) lastRequest(t *testing.T) protocol.Request {
	t.Helper()
	select {
	case req := <-b.requests:
		return req
	case <-time.After(5 * time.Second):
		t.Fatal("broker got no request")
		return protocol.Request{}
	}
}

func connect(t *testing.T, socketPath string) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := newServer(socketPath).Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { serverSession.Close() })
	session, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })
	return session
}

func callTool(t *testing.T, session *mcp.ClientSession, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool %s: %v", name, err)
	}
	return res
}

func text(res *mcp.CallToolResult) string {
	var parts []string
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			parts = append(parts, tc.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func structured(t *testing.T, res *mcp.CallToolResult, into any) {
	t.Helper()
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, into); err != nil {
		t.Fatalf("structured content %s: %v", raw, err)
	}
}

func TestListToolsShowsFiveToolsWithSchemas(t *testing.T) {
	// given
	session := connect(t, startBroker(t, protocol.Response{}).socketPath)

	// when
	res, err := session.ListTools(context.Background(), nil)

	// then
	if err != nil {
		t.Fatal(err)
	}
	schemas := map[string]map[string]any{}
	for _, tool := range res.Tools {
		if tool.Description == "" {
			t.Errorf("%s has no description", tool.Name)
		}
		if tool.OutputSchema == nil {
			t.Errorf("%s has no output schema", tool.Name)
		}
		schemas[tool.Name] = tool.InputSchema.(map[string]any)
	}
	names := slices.Sorted(func(yield func(string) bool) {
		for n := range schemas {
			if !yield(n) {
				return
			}
		}
	})
	want := []string{"fetch_status", "job_status", "list_actions", "request_fetch", "run_action"}
	if !slices.Equal(names, want) {
		t.Fatalf("tools = %v, want %v", names, want)
	}

	assertProps(t, schemas["list_actions"])
	assertProps(t, schemas["run_action"], "name", "params")
	assertRequired(t, schemas["run_action"], "name")
	assertProps(t, schemas["job_status"], "job_id")
	assertRequired(t, schemas["job_status"], "job_id")
	assertProps(t, schemas["request_fetch"], "coordinates", "ecosystem")
	assertRequired(t, schemas["request_fetch"], "coordinates", "ecosystem")
	assertProps(t, schemas["fetch_status"], "request_id")
	assertRequired(t, schemas["fetch_status"], "request_id")

	ecosystem := schemas["request_fetch"]["properties"].(map[string]any)["ecosystem"].(map[string]any)
	if got := ecosystem["enum"]; !slices.Equal(toStrings(got), []string{"maven", "npm", "image"}) {
		t.Errorf("ecosystem enum = %v", got)
	}
	params := schemas["run_action"]["properties"].(map[string]any)["params"].(map[string]any)
	if !strings.Contains(params["description"].(string), "pattern") {
		t.Errorf("params description = %q", params["description"])
	}
}

func assertProps(t *testing.T, schema map[string]any, want ...string) {
	t.Helper()
	props, _ := schema["properties"].(map[string]any)
	var got []string
	for name, p := range props {
		got = append(got, name)
		if d, _ := p.(map[string]any)["description"].(string); d == "" {
			t.Errorf("property %s has no description", name)
		}
	}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("properties = %v, want %v", got, want)
	}
}

func assertRequired(t *testing.T, schema map[string]any, want ...string) {
	t.Helper()
	got := toStrings(schema["required"])
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("required = %v, want %v", got, want)
	}
}

func toStrings(v any) []string {
	var out []string
	list, _ := v.([]any)
	for _, s := range list {
		out = append(out, s.(string))
	}
	return out
}

func TestListActionsForwardsAndReturnsActions(t *testing.T) {
	// given
	actions := []definitions.ListedAction{{
		Name:        "test",
		Description: "runs tests",
		Params:      map[string]definitions.ListedParam{"filter": {Pattern: "^[a-z]+$", Optional: true, Description: "test filter"}},
	}}
	broker := startBroker(t, protocol.Response{Actions: actions})
	session := connect(t, broker.socketPath)
	wd, _ := os.Getwd()

	// when
	res := callTool(t, session, "list_actions", map[string]any{})

	// then
	if res.IsError {
		t.Fatalf("isError: %s", text(res))
	}
	req := broker.lastRequest(t)
	if req.Op != protocol.OpListActions || req.Cwd != wd {
		t.Fatalf("req = %+v", req)
	}
	var out ListActionsOutput
	structured(t, res, &out)
	if len(out.Actions) != 1 || out.Actions[0].Name != "test" || out.Actions[0].Params["filter"].Pattern != "^[a-z]+$" {
		t.Fatalf("out = %+v", out)
	}
	if !strings.Contains(text(res), `"runs tests"`) {
		t.Fatalf("text = %s", text(res))
	}
}

func TestListActionsWithNoActionsReturnsEmptyList(t *testing.T) {
	// given
	session := connect(t, startBroker(t, protocol.Response{}).socketPath)

	// when
	res := callTool(t, session, "list_actions", nil)

	// then
	if res.IsError {
		t.Fatalf("isError: %s", text(res))
	}
	if !strings.Contains(text(res), `"actions":[]`) {
		t.Fatalf("text = %s", text(res))
	}
}

func TestRunActionForwardsNameAndParams(t *testing.T) {
	// given
	exit := 0
	broker := startBroker(t, protocol.Response{Job: &protocol.Job{ID: "j1", State: protocol.JobSucceeded, ExitCode: &exit, OutputTail: "ok"}})
	session := connect(t, broker.socketPath)

	// when
	res := callTool(t, session, "run_action", map[string]any{"name": "test", "params": map[string]any{"filter": "unit"}})

	// then
	if res.IsError {
		t.Fatalf("isError: %s", text(res))
	}
	req := broker.lastRequest(t)
	if req.Op != protocol.OpRunAction || req.Name != "test" || req.Params["filter"] != "unit" || req.Cwd == "" {
		t.Fatalf("req = %+v", req)
	}
	var job protocol.Job
	structured(t, res, &job)
	if job.ID != "j1" || job.State != protocol.JobSucceeded || job.ExitCode == nil || *job.ExitCode != 0 || job.OutputTail != "ok" {
		t.Fatalf("job = %+v", job)
	}
}

func TestRunActionWithoutParams(t *testing.T) {
	// given
	broker := startBroker(t, protocol.Response{Job: &protocol.Job{ID: "j1", State: protocol.JobRunning}})
	session := connect(t, broker.socketPath)

	// when
	res := callTool(t, session, "run_action", map[string]any{"name": "build"})

	// then
	if res.IsError {
		t.Fatalf("isError: %s", text(res))
	}
	if req := broker.lastRequest(t); req.Name != "build" || len(req.Params) != 0 {
		t.Fatalf("req = %+v", req)
	}
}

func TestJobStatusForwardsJobID(t *testing.T) {
	// given
	broker := startBroker(t, protocol.Response{Job: &protocol.Job{ID: "j1", State: protocol.JobFailed, LogPath: "/log"}})
	session := connect(t, broker.socketPath)

	// when
	res := callTool(t, session, "job_status", map[string]any{"job_id": "j1"})

	// then
	if res.IsError {
		t.Fatalf("isError: %s", text(res))
	}
	if req := broker.lastRequest(t); req.Op != protocol.OpJobStatus || req.JobID != "j1" || req.Cwd == "" {
		t.Fatalf("req = %+v", req)
	}
	var job protocol.Job
	structured(t, res, &job)
	if job.State != protocol.JobFailed || job.LogPath != "/log" {
		t.Fatalf("job = %+v", job)
	}
}

func TestRequestFetchForwardsEcosystemAndCoordinates(t *testing.T) {
	// given
	broker := startBroker(t, protocol.Response{Fetch: &protocol.Fetch{ID: "f1", State: protocol.FetchPending}})
	session := connect(t, broker.socketPath)

	// when
	res := callTool(t, session, "request_fetch", map[string]any{"ecosystem": "maven", "coordinates": []string{"g:a:1", "g:b:2"}})

	// then
	if res.IsError {
		t.Fatalf("isError: %s", text(res))
	}
	req := broker.lastRequest(t)
	if req.Op != protocol.OpRequestFetch || req.Ecosystem != "maven" || !slices.Equal(req.Coordinates, []string{"g:a:1", "g:b:2"}) || req.Cwd == "" {
		t.Fatalf("req = %+v", req)
	}
	var fetch protocol.Fetch
	structured(t, res, &fetch)
	if fetch.ID != "f1" || fetch.State != protocol.FetchPending {
		t.Fatalf("fetch = %+v", fetch)
	}
}

func TestRequestFetchRejectsUnknownEcosystem(t *testing.T) {
	// given
	broker := startBroker(t, protocol.Response{Fetch: &protocol.Fetch{ID: "f1", State: protocol.FetchPending}})
	session := connect(t, broker.socketPath)

	// when
	res := callTool(t, session, "request_fetch", map[string]any{"ecosystem": "pip", "coordinates": []string{"x"}})

	// then
	if !res.IsError {
		t.Fatalf("want isError, got %s", text(res))
	}
	select {
	case req := <-broker.requests:
		t.Fatalf("forwarded %+v", req)
	default:
	}
}

func TestFetchStatusForwardsRequestID(t *testing.T) {
	// given
	broker := startBroker(t, protocol.Response{Fetch: &protocol.Fetch{ID: "f1", State: protocol.FetchRejected, Detail: "no"}})
	session := connect(t, broker.socketPath)

	// when
	res := callTool(t, session, "fetch_status", map[string]any{"request_id": "f1"})

	// then
	if res.IsError {
		t.Fatalf("isError: %s", text(res))
	}
	if req := broker.lastRequest(t); req.Op != protocol.OpFetchStatus || req.RequestID != "f1" || req.Cwd == "" {
		t.Fatalf("req = %+v", req)
	}
	var fetch protocol.Fetch
	structured(t, res, &fetch)
	if fetch.State != protocol.FetchRejected || fetch.Detail != "no" {
		t.Fatalf("fetch = %+v", fetch)
	}
}

func TestBrokerErrorBecomesToolError(t *testing.T) {
	// given
	session := connect(t, startBroker(t, protocol.Response{Error: "untrusted: rollback"}).socketPath)

	// when
	res := callTool(t, session, "list_actions", nil)

	// then
	if !res.IsError || text(res) != "untrusted: rollback" {
		t.Fatalf("isError=%v text=%q", res.IsError, text(res))
	}
}

func TestMissingJobInResponseIsToolError(t *testing.T) {
	// given
	session := connect(t, startBroker(t, protocol.Response{}).socketPath)

	// when
	res := callTool(t, session, "job_status", map[string]any{"job_id": "j1"})

	// then
	if !res.IsError {
		t.Fatalf("want isError, got %s", text(res))
	}
}

func TestBrokerDownBecomesToolErrorWithHint(t *testing.T) {
	// given
	session := connect(t, filepath.Join(t.TempDir(), "missing.sock"))

	// when
	res := callTool(t, session, "job_status", map[string]any{"job_id": "j1"})

	// then
	if !res.IsError || !strings.Contains(text(res), "actionbox serve is not running") {
		t.Fatalf("isError=%v text=%q", res.IsError, text(res))
	}
}

func TestRunServesOverStreams(t *testing.T) {
	// given
	broker := startBroker(t, protocol.Response{Job: &protocol.Job{ID: "j1", State: protocol.JobRunning}})
	clientToServerR, clientToServerW := io.Pipe()
	serverToClientR, serverToClientW := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, broker.socketPath, clientToServerR, serverToClientW) }()
	session, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).
		Connect(ctx, &mcp.IOTransport{Reader: serverToClientR, Writer: clientToServerW}, nil)
	if err != nil {
		t.Fatal(err)
	}

	// when
	res := callTool(t, session, "job_status", map[string]any{"job_id": "j1"})

	// then
	if res.IsError || session.InitializeResult().ServerInfo.Name != "actionbox" {
		t.Fatalf("isError=%v text=%q info=%+v", res.IsError, text(res), session.InitializeResult().ServerInfo)
	}
	session.Close()
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return")
	}
}
