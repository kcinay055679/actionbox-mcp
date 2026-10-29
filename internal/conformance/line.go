// Package conformance runs the spec §9 isolation probes P1–P16 through the engine.
package conformance

import (
	"bufio"
	"bytes"
	"fmt"
	"strings"
)

const linePrefix = "PROBE "

type Outcome string

const (
	OutcomeOK   Outcome = "ok"
	OutcomeFail Outcome = "fail"
	OutcomeSkip Outcome = "skip"
)

type Line struct {
	Name    string
	Outcome Outcome
	Detail  string
}

func (l Line) String() string {
	detail := strings.Join(strings.Fields(l.Detail), " ")
	return fmt.Sprintf("%s%s %s %s", linePrefix, l.Name, l.Outcome, detail)
}

func ParseLine(output []byte, name string) (Line, bool) {
	var found Line
	ok := false
	sc := bufio.NewScanner(bytes.NewReader(output))
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		rest, isProbe := strings.CutPrefix(sc.Text(), linePrefix)
		if !isProbe {
			continue
		}
		fields := strings.SplitN(rest, " ", 3)
		if len(fields) < 2 || fields[0] != name {
			continue
		}
		outcome := Outcome(fields[1])
		if outcome != OutcomeOK && outcome != OutcomeFail && outcome != OutcomeSkip {
			continue
		}
		found = Line{Name: name, Outcome: outcome}
		if len(fields) == 3 {
			found.Detail = fields[2]
		}
		ok = true
	}
	return found, ok
}
