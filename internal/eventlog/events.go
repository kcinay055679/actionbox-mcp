package eventlog

type JobStart struct {
	Project           string            `json:"project"`
	JobID             string            `json:"job_id"`
	Action            string            `json:"action"`
	Params            map[string]string `json:"params,omitempty"`
	DefinitionsCommit string            `json:"definitions_commit"`
}

func (JobStart) Kind() string { return "job_start" }

type JobEnd struct {
	Project           string            `json:"project"`
	JobID             string            `json:"job_id"`
	Action            string            `json:"action"`
	Params            map[string]string `json:"params,omitempty"`
	DefinitionsCommit string            `json:"definitions_commit"`
	State             string            `json:"state"`
	ExitCode          int               `json:"exit_code"`
}

func (JobEnd) Kind() string { return "job_end" }

type FetchDecision struct {
	Project     string        `json:"project"`
	RequestID   string        `json:"request_id"`
	Ecosystem   string        `json:"ecosystem"`
	Coordinates []string      `json:"coordinates"`
	Decision    string        `json:"decision"`
	Result      string        `json:"result,omitempty"`
	Detail      string        `json:"detail,omitempty"`
	Files       []FetchedFile `json:"files,omitempty"`
}

func (FetchDecision) Kind() string { return "fetch_decision" }

type FetchedFile struct {
	Coordinate string `json:"coordinate"`
	Path       string `json:"path"`
	Integrity  string `json:"integrity"`
	Size       int64  `json:"size"`
}
