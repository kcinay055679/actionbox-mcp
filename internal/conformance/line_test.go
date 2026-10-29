package conformance

import "testing"

func TestLineRoundTrip(t *testing.T) {
	// given
	out := []byte("noise\n" +
		Line{Name: "env", Outcome: OutcomeFail, Detail: "first"}.String() + "\n" +
		Line{Name: "other", Outcome: OutcomeOK}.String() + "\n" +
		"PROBE env bogus x\n" +
		Line{Name: "env", Outcome: OutcomeOK, Detail: "multi\nline  detail"}.String() + "\n")

	// when
	got, found := ParseLine(out, "env")

	// then
	if !found || got.Outcome != OutcomeOK || got.Detail != "multi line detail" {
		t.Errorf("got %+v, %v", got, found)
	}
	if _, found := ParseLine(out, "missing"); found {
		t.Error("found a line for missing probe")
	}
}
