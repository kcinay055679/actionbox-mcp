package conformance

import (
	"bytes"
	"strings"
	"testing"
)

func TestScanAgentEnvWarnsOnSecretNamesWithoutValues(t *testing.T) {
	// when
	got := ScanAgentEnv([]string{
		"PATH=/bin", "GITHUB_TOKEN=ghp_value", "AWS_REGION=eu", "my_api_key=v", "DB_PASSWORD=pw", "GH_HOST=x", "HOME=/h",
	})

	// then
	var names []string
	for _, r := range got {
		if r.Status != Warn || r.Mode != AgentEnvMode {
			t.Errorf("result %+v", r)
		}
		if strings.Contains(r.Detail, "ghp_value") {
			t.Error("value leaked")
		}
		names = append(names, strings.Fields(r.Detail)[0])
	}
	want := "AWS_REGION DB_PASSWORD GH_HOST GITHUB_TOKEN my_api_key"
	if strings.Join(names, " ") != want {
		t.Errorf("names = %v, want %s", names, want)
	}
}

func TestScanAgentEnvCleanPasses(t *testing.T) {
	got := ScanAgentEnv([]string{"PATH=/bin"})
	if len(got) != 1 || got[0].Status != Pass {
		t.Errorf("got %+v", got)
	}
}

func TestReportLineAndSummary(t *testing.T) {
	// given
	results := []Result{
		{ID: "P1", Mode: ModeName(false), Status: Pass, Detail: "ok"},
		{ID: "P2", Mode: ModeName(true), Status: Fail, Detail: "writable"},
		{ID: "P15", Mode: ModeName(true), Status: Skip},
	}
	var buf bytes.Buffer

	// when
	WriteResult(&buf, results[1])
	WriteSummary(&buf, results)

	// then
	out := buf.String()
	if !strings.HasPrefix(out, "P2          containers=true  FAIL  writable\n") {
		t.Errorf("line = %q", out)
	}
	if !strings.Contains(out, "1 pass, 1 fail, 0 warn, 1 skip") {
		t.Errorf("summary = %q", out)
	}
}
