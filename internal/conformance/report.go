package conformance

import (
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"
)

type Status string

const (
	Pass Status = "PASS"
	Fail Status = "FAIL"
	Warn Status = "WARN"
	Skip Status = "SKIP"
)

type Result struct {
	ID     string
	Mode   string
	Status Status
	Detail string
}

func ModeName(containers bool) string { return fmt.Sprintf("containers=%t", containers) }

const AgentEnvMode = "agent-env"

func (r Result) String() string {
	return fmt.Sprintf("%-11s %-16s %-4s  %s", r.ID, r.Mode, r.Status, r.Detail)
}

func WriteResult(w io.Writer, r Result) {
	fmt.Fprintln(w, r)
}

func Count(results []Result, s Status) int {
	n := 0
	for _, r := range results {
		if r.Status == s {
			n++
		}
	}
	return n
}

func WriteSummary(w io.Writer, results []Result) {
	fmt.Fprintf(w, "\nconformance: %d pass, %d fail, %d warn, %d skip\n",
		Count(results, Pass), Count(results, Fail), Count(results, Warn), Count(results, Skip))
}

var (
	secretNamePattern = regexp.MustCompile(`TOKEN|SECRET|KEY|PASSWORD`)
	secretPrefixes    = []string{"AWS_", "GH_", "GITHUB_"}
)

// R36: warn, never fail; names only, values are the secrets
func ScanAgentEnv(environ []string) []Result {
	var names []string
	for _, kv := range environ {
		name, _, _ := strings.Cut(kv, "=")
		upper := strings.ToUpper(name)
		if secretNamePattern.MatchString(upper) || slices.ContainsFunc(secretPrefixes, func(p string) bool {
			return strings.HasPrefix(upper, p)
		}) {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	if len(names) == 0 {
		return []Result{{ID: "P16", Mode: AgentEnvMode, Status: Pass, Detail: "no token- or key-like variable names"}}
	}
	var out []Result
	for _, n := range names {
		out = append(out, Result{ID: "P16", Mode: AgentEnvMode, Status: Warn,
			Detail: n + " is visible to the agent; remove it from the agent's environment if it is a credential"})
	}
	return out
}
