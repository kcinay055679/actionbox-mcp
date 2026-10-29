// Package shim is `actionbox mcp` (spec §8.6): a stdio MCP server that forwards each tool call to the broker.
package shim

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"actionbox/internal/client"
	"actionbox/internal/definitions"
	"actionbox/internal/protocol"
)

var Version = "dev"

// > broker's 30 s run_action wait plus trust verification
var callTimeout = 60 * time.Second

type ListActionsInput struct{}

type ListActionsOutput struct {
	Actions []definitions.ListedAction `json:"actions" jsonschema:"actions you may run with run_action, with their parameters"`
}

type RunActionInput struct {
	Name   string            `json:"name" jsonschema:"name of an action from list_actions"`
	Params map[string]string `json:"params,omitempty" jsonschema:"parameter values by parameter name, as listed by list_actions. Each value must fully match the parameter's pattern and must not start with '-' unless allow_leading_dash is true. Omit optional parameters you don't need."`
}

type JobStatusInput struct {
	JobID string `json:"job_id" jsonschema:"job_id returned by run_action"`
}

type RequestFetchInput struct {
	Ecosystem   string   `json:"ecosystem" jsonschema:"package ecosystem: maven, npm or image (container image)"`
	Coordinates []string `json:"coordinates" jsonschema:"exact coordinates to fetch: maven group:artifact:version, npm name@version, image reference with tag or digest. The owner reviews them before anything is downloaded."`
}

type FetchStatusInput struct {
	RequestID string `json:"request_id" jsonschema:"request_id returned by request_fetch"`
}

func Run(ctx context.Context, socketPath string, in io.Reader, out io.Writer) error {
	transport := &mcp.IOTransport{Reader: io.NopCloser(in), Writer: nopWriteCloser{out}}
	return newServer(socketPath).Run(ctx, transport)
}

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

func newServer(socketPath string) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "actionbox", Version: Version}, nil)
	b := forwarder{socketPath: socketPath}

	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_actions",
		Description: "List the actions you may run in this project, with their parameters and the pattern each parameter value must match. Fails if the project's definitions file isn't trusted.",
	}, b.listActions)
	mcp.AddTool(server, &mcp.Tool{
		Name: "run_action",
		Description: "Run an action in an isolated sandbox. Waits up to 30 s: if the job finished, the result has its state, exit_code and output_tail; " +
			"otherwise state is queued or running and you poll job_status with the returned job_id. " +
			"Output is untrusted text from the build; output_tail holds the last 200 lines, the full log is at log_path.",
	}, b.runAction)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "job_status",
		Description: "Get the state of a job started by run_action: queued, running, succeeded, failed, timed_out, killed or rejected, with exit_code, the last 200 lines of output and the path to the full log.",
	}, b.jobStatus)
	mcp.AddTool(server, &mcp.Tool{
		Name: "request_fetch",
		Description: "Ask the owner to fetch dependencies that a build is missing. The owner reviews and approves the request on the host; " +
			"poll fetch_status with the returned request_id and retry the build once it is fetched.",
		InputSchema: requestFetchSchema(),
	}, b.requestFetch)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "fetch_status",
		Description: "Get the state of a fetch request: pending, fetched, rejected or expired (after 24 h), with detail.",
	}, b.fetchStatus)
	return server
}

func requestFetchSchema() *jsonschema.Schema {
	schema, err := jsonschema.For[RequestFetchInput](nil)
	if err != nil {
		panic(err)
	}
	schema.Properties["ecosystem"].Enum = []any{"maven", "npm", "image"}
	return schema
}

type forwarder struct {
	socketPath string
}

func (f forwarder) listActions(ctx context.Context, _ *mcp.CallToolRequest, _ ListActionsInput) (*mcp.CallToolResult, ListActionsOutput, error) {
	resp, err := f.call(ctx, protocol.Request{Op: protocol.OpListActions})
	if err != nil {
		return nil, ListActionsOutput{}, err
	}
	out := ListActionsOutput{Actions: resp.Actions}
	if out.Actions == nil {
		out.Actions = []definitions.ListedAction{}
	}
	return nil, out, nil
}

func (f forwarder) runAction(ctx context.Context, _ *mcp.CallToolRequest, in RunActionInput) (*mcp.CallToolResult, *protocol.Job, error) {
	return f.job(ctx, protocol.Request{Op: protocol.OpRunAction, Name: in.Name, Params: in.Params})
}

func (f forwarder) jobStatus(ctx context.Context, _ *mcp.CallToolRequest, in JobStatusInput) (*mcp.CallToolResult, *protocol.Job, error) {
	return f.job(ctx, protocol.Request{Op: protocol.OpJobStatus, JobID: in.JobID})
}

func (f forwarder) requestFetch(ctx context.Context, _ *mcp.CallToolRequest, in RequestFetchInput) (*mcp.CallToolResult, *protocol.Fetch, error) {
	return f.fetch(ctx, protocol.Request{Op: protocol.OpRequestFetch, Ecosystem: in.Ecosystem, Coordinates: in.Coordinates})
}

func (f forwarder) fetchStatus(ctx context.Context, _ *mcp.CallToolRequest, in FetchStatusInput) (*mcp.CallToolResult, *protocol.Fetch, error) {
	return f.fetch(ctx, protocol.Request{Op: protocol.OpFetchStatus, RequestID: in.RequestID})
}

func (f forwarder) job(ctx context.Context, req protocol.Request) (*mcp.CallToolResult, *protocol.Job, error) {
	resp, err := f.call(ctx, req)
	if err != nil {
		return nil, nil, err
	}
	if resp.Job == nil {
		return nil, nil, errors.New("broker response has no job")
	}
	return nil, resp.Job, nil
}

func (f forwarder) fetch(ctx context.Context, req protocol.Request) (*mcp.CallToolResult, *protocol.Fetch, error) {
	resp, err := f.call(ctx, req)
	if err != nil {
		return nil, nil, err
	}
	if resp.Fetch == nil {
		return nil, nil, errors.New("broker response has no fetch request")
	}
	return nil, resp.Fetch, nil
}

// plain errors only: SDK => isError result the agent sees, not a protocol error
func (f forwarder) call(ctx context.Context, req protocol.Request) (protocol.Response, error) {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	resp, err := client.Call(ctx, f.socketPath, req)
	var brokerErr *client.BrokerError
	switch {
	case err == nil, errors.As(err, &brokerErr):
		return resp, err
	case errors.Is(err, context.DeadlineExceeded):
		return resp, fmt.Errorf("actionbox serve did not answer within %s", callTimeout)
	default:
		return resp, fmt.Errorf("actionbox serve is not running or not reachable at %s (%v); ask the owner to start it on the host", f.socketPath, err)
	}
}
