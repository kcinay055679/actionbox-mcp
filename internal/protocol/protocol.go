// Package protocol is the shim ↔ broker wire format (spec §8.5): one newline-delimited JSON request, one response.
package protocol

import "actionbox/internal/definitions"

type Op string

const (
	OpListActions  Op = "list_actions"
	OpRunAction    Op = "run_action"
	OpJobStatus    Op = "job_status"
	OpRequestFetch Op = "request_fetch"
	OpFetchStatus  Op = "fetch_status"
)

type JobState string

const (
	JobQueued    JobState = "queued"
	JobRunning   JobState = "running"
	JobSucceeded JobState = "succeeded"
	JobFailed    JobState = "failed"
	JobTimedOut  JobState = "timed_out"
	JobKilled    JobState = "killed"
	JobRejected  JobState = "rejected"
)

func (s JobState) Done() bool {
	return s != JobQueued && s != JobRunning
}

type FetchState string

const (
	FetchPending  FetchState = "pending"
	FetchFetched  FetchState = "fetched"
	FetchRejected FetchState = "rejected"
	FetchExpired  FetchState = "expired"
)

// Request carries the shim's cwd on every op; the broker maps it to a project and trusts nothing else in it.
type Request struct {
	Op  Op     `json:"op"`
	Cwd string `json:"cwd"`

	Name   string            `json:"name,omitempty"`
	Params map[string]string `json:"params,omitempty"`

	JobID string `json:"job_id,omitempty"`

	Ecosystem   string   `json:"ecosystem,omitempty"`
	Coordinates []string `json:"coordinates,omitempty"`

	RequestID string `json:"request_id,omitempty"`
}

type Response struct {
	Error string `json:"error,omitempty"`

	Actions []definitions.ListedAction `json:"actions,omitempty"`
	Job     *Job                       `json:"job,omitempty"`
	Fetch   *Fetch                     `json:"fetch,omitempty"`
}

type Job struct {
	ID         string   `json:"job_id"`
	State      JobState `json:"state"`
	ExitCode   *int     `json:"exit_code,omitempty"`
	OutputTail string   `json:"output_tail,omitempty"`
	LogPath    string   `json:"log_path,omitempty"`
	Detail     string   `json:"detail,omitempty"`
}

type Fetch struct {
	ID     string     `json:"request_id"`
	State  FetchState `json:"state"`
	Detail string     `json:"detail,omitempty"`
}

const (
	MaxRequestBytes = 64 << 10
	TailLines       = 200
	TailBytes       = 16 << 10
)
