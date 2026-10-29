package definitions

import (
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"actionbox/internal/limits"
)

func parseSpecExample(t *testing.T) *Definitions {
	t.Helper()
	data, err := os.ReadFile("testdata/spec-example.toml")
	if err != nil {
		t.Fatal(err)
	}
	defs, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse(spec example): %v", err)
	}
	return defs
}

func TestParseSpecExample(t *testing.T) {
	defs := parseSpecExample(t)

	if got, want := defs.Names(), []string{"fetch", "format", "lint", "npm-ci", "test"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Names() = %v, want %v", got, want)
	}

	test := defs.Actions["test"]
	if test.Invoker != InvokerAgent || test.Kind != KindRun || !test.Containers {
		t.Errorf("test: invoker=%q kind=%q containers=%v", test.Invoker, test.Kind, test.Containers)
	}
	if want := []string{"target"}; !reflect.DeepEqual(test.Writes, want) {
		t.Errorf("test writes = %v, want %v", test.Writes, want)
	}
	if want := (limits.Limits{Timeout: 20 * time.Minute, MemoryMax: 6 << 30}); test.Limits != want {
		t.Errorf("test limits = %+v, want %+v", test.Limits, want)
	}
	p := test.Params["test_class"]
	if p.Pattern != `[A-Za-z0-9_.#*]+` || !p.Optional || p.AllowLeadingDash || p.Description != "Surefire -Dtest filter" {
		t.Errorf("test_class param = %+v", p)
	}

	lint := defs.Actions["lint"]
	if lint.Containers || lint.Kind != KindRun || len(lint.Writes) != 0 {
		t.Errorf("lint defaults: containers=%v kind=%q writes=%v", lint.Containers, lint.Kind, lint.Writes)
	}

	fetch := defs.Actions["fetch"]
	if fetch.Invoker != InvokerOwner || fetch.Kind != KindCoordinateFetch || fetch.ArgvTemplate != nil {
		t.Errorf("fetch = %+v", fetch)
	}
}

func TestParseRejects(t *testing.T) {
	tests := []struct {
		name    string
		toml    string
		wantMsg string
	}{
		{name: "not toml", toml: `schema_version = `, wantMsg: "toml"},
		{name: "missing schema_version", toml: `[actions.a]
description = "d"
invoker = "agent"
argv = ["true"]`, wantMsg: "schema_version"},
		{name: "wrong schema_version", toml: `schema_version = 2`, wantMsg: "schema_version"},
		{name: "unknown top-level key", toml: `schema_version = 1
extra = 1`, wantMsg: "extra"},
		{name: "unknown action key", toml: action(`engine = "fence"`), wantMsg: "engine"},
		{name: "unknown param key", toml: action(`argv = ["echo", "{p}"]
[actions.a.params.p]
pattern = "x"
required = true`), wantMsg: "required"},
		{name: "bad action name", toml: `schema_version = 1
[actions.Bad_Name]
description = "d"
invoker = "agent"
argv = ["true"]`, wantMsg: "Bad_Name"},
		{name: "missing description", toml: `schema_version = 1
[actions.a]
invoker = "agent"
argv = ["true"]`, wantMsg: "description"},
		{name: "missing invoker", toml: `schema_version = 1
[actions.a]
description = "d"
argv = ["true"]`, wantMsg: "invoker"},
		{name: "bad invoker", toml: `schema_version = 1
[actions.a]
description = "d"
invoker = "user"
argv = ["true"]`, wantMsg: "invoker"},
		{name: "bad kind", toml: action(`kind = "fetch"`), wantMsg: "kind"},
		{name: "containers not bool", toml: action(`containers = "yes"`), wantMsg: "containers"},
		{name: "missing argv", toml: `schema_version = 1
[actions.a]
description = "d"
invoker = "agent"`, wantMsg: "argv"},
		{name: "empty argv", toml: `schema_version = 1
[actions.a]
description = "d"
invoker = "agent"
argv = []`, wantMsg: "argv"},
		{name: "argv not array", toml: `schema_version = 1
[actions.a]
description = "d"
invoker = "agent"
argv = "mvn test"`, wantMsg: "argv"},
		{name: "empty argv[0]", toml: `schema_version = 1
[actions.a]
description = "d"
invoker = "agent"
argv = [""]`, wantMsg: "argv"},
		{name: "argv NUL", toml: `schema_version = 1
[actions.a]
description = "d"
invoker = "agent"
argv = ["echo", "a\u0000b"]`, wantMsg: "NUL"},
		{name: "undeclared placeholder", toml: action(`argv = ["echo", "{nope}"]`), wantMsg: "nope"},
		{name: "unused param", toml: action(`argv = ["echo"]
[actions.a.params.p]
pattern = "x"`), wantMsg: `"p"`},
		{name: "bad param name", toml: action(`argv = ["echo", "{a-b}"]
[actions.a.params.a-b]
pattern = "x"`), wantMsg: "a-b"},
		{name: "missing pattern", toml: action(`argv = ["echo", "{p}"]
[actions.a.params.p]
optional = true`), wantMsg: "pattern"},
		{name: "pattern does not compile", toml: action(`argv = ["echo", "{p}"]
[actions.a.params.p]
pattern = "[a-"`), wantMsg: "pattern"},
		{name: "coordinate-fetch by agent", toml: `schema_version = 1
[actions.f]
description = "d"
invoker = "agent"
kind = "coordinate-fetch"`, wantMsg: "invoker"},
		{name: "coordinate-fetch with argv", toml: `schema_version = 1
[actions.f]
description = "d"
invoker = "owner"
kind = "coordinate-fetch"
argv = ["mvn"]`, wantMsg: "argv"},
		{name: "coordinate-fetch with writes", toml: `schema_version = 1
[actions.f]
description = "d"
invoker = "owner"
kind = "coordinate-fetch"
writes = ["target"]`, wantMsg: "writes"},
		{name: "coordinate-fetch with containers", toml: `schema_version = 1
[actions.f]
description = "d"
invoker = "owner"
kind = "coordinate-fetch"
containers = true`, wantMsg: "containers"},
		{name: "coordinate-fetch with params", toml: `schema_version = 1
[actions.f]
description = "d"
invoker = "owner"
kind = "coordinate-fetch"
[actions.f.params.p]
pattern = "x"`, wantMsg: "params"},
		{name: "bad timeout", toml: action(`timeout = "soon"`), wantMsg: "timeout"},
		{name: "zero timeout", toml: action(`timeout = "0s"`), wantMsg: "timeout"},
		{name: "bad memory_max", toml: action(`memory_max = "6GB"`), wantMsg: "memory_max"},
		{name: "bad tasks_max", toml: action(`tasks_max = -1`), wantMsg: "tasks_max"},
		{name: "bad cpu_quota", toml: action(`cpu_quota = "4"`), wantMsg: "cpu_quota"},
		{name: "writes absolute", toml: writes(`"/tmp"`), wantMsg: "/tmp"},
		{name: "writes dotdot", toml: writes(`"../sibling"`), wantMsg: "../sibling"},
		{name: "writes inner dotdot", toml: writes(`"a/../b"`), wantMsg: "a/../b"},
		{name: "writes star", toml: writes(`"tar*"`), wantMsg: "tar*"},
		{name: "writes question", toml: writes(`"t?rget"`), wantMsg: "t?rget"},
		{name: "writes bracket", toml: writes(`"[ab]"`), wantMsg: "[ab]"},
		{name: "writes brace", toml: writes(`"{a,b}"`), wantMsg: "{a,b}"},
		{name: "writes empty", toml: writes(`""`), wantMsg: "writes"},
		{name: "writes dot", toml: writes(`"."`), wantMsg: `"."`},
		{name: "writes trailing slash", toml: writes(`"target/"`), wantMsg: "target/"},
		{name: "writes dot prefix", toml: writes(`"./target"`), wantMsg: "./target"},
		{name: "writes double slash", toml: writes(`"a//b"`), wantMsg: "a//b"},
		{name: "writes protected", toml: writes(`".git"`), wantMsg: ".git"},
		{name: "writes protected file", toml: writes(`".agent-actions.toml"`), wantMsg: ".agent-actions.toml"},
		{name: "writes inside protected", toml: writes(`".git/hooks"`), wantMsg: ".git/hooks"},
		{name: "writes inside .mvn", toml: writes(`".mvn/wrapper"`), wantMsg: ".mvn/wrapper"},
		{name: "writes duplicate", toml: writes(`"target", "target"`), wantMsg: "duplicate"},
		{name: "writes NUL", toml: writes(`"a\u0000b"`), wantMsg: "writes"},
		{name: "writes not strings", toml: writes(`1`), wantMsg: "writes"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte(tt.toml))
			if err == nil {
				t.Fatal("want error")
			}
			if !errors.Is(err, ErrInvalid) {
				t.Errorf("error %v does not wrap ErrInvalid", err)
			}
			if !strings.Contains(err.Error(), tt.wantMsg) {
				t.Errorf("error %q does not mention %q", err, tt.wantMsg)
			}
		})
	}
}

func TestParseNamesActionInError(t *testing.T) {
	_, err := Parse([]byte(writes(`"/abs"`)))
	if err == nil || !strings.Contains(err.Error(), `action "a"`) {
		t.Errorf("error %v does not name action", err)
	}
}

func TestParseAcceptsEdgeCases(t *testing.T) {
	tests := []struct {
		name string
		toml string
	}{
		{name: "nested writes", toml: writes(`"build/out", "node_modules"`)},
		{name: "protected name below root", toml: writes(`"src/.git"`)},
		{name: "literal braces", toml: action(`argv = ["find", ".", "-exec", "echo", "{}", ";"]`)},
		{name: "placeholder twice", toml: action(`argv = ["echo", "{p}", "--x={p}"]
[actions.a.params.p]
pattern = "x"`)},
		{name: "no actions", toml: `schema_version = 1`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Parse([]byte(tt.toml)); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestArgv(t *testing.T) {
	defs := mustParse(t, `schema_version = 1
[actions.a]
description = "d"
invoker = "agent"
argv = ["tool", "-Dtest={cls}", "--name", "{name}", "{dash}", "{cls}#{name}"]
[actions.a.params.cls]
pattern = '[A-Za-z.]+'
optional = true
[actions.a.params.name]
pattern = '[a-z {}-]+'
[actions.a.params.dash]
pattern = '-?[a-z]+'
optional = true
allow_leading_dash = true
`)
	a := defs.Actions["a"]

	tests := []struct {
		name    string
		params  map[string]string
		want    []string
		wantErr string
	}{
		{
			name:   "all params",
			params: map[string]string{"cls": "Foo", "name": "bar", "dash": "-v"},
			want:   []string{"tool", "-Dtest=Foo", "--name", "bar", "-v", "Foo#bar"},
		},
		{
			name:   "omitted optional drops its elements",
			params: map[string]string{"name": "bar"},
			want:   []string{"tool", "--name", "bar"},
		},
		{
			name:   "value with space not split",
			params: map[string]string{"name": "a b"},
			want:   []string{"tool", "--name", "a b"},
		},
		{
			name:   "value not re-substituted",
			params: map[string]string{"name": "{cls}", "cls": "X"},
			want:   []string{"tool", "-Dtest=X", "--name", "{cls}", "X#{cls}"},
		},
		{name: "missing required", params: map[string]string{}, wantErr: `"name"`},
		{name: "nil params missing required", params: nil, wantErr: `"name"`},
		{name: "unknown param", params: map[string]string{"name": "a", "x": "1"}, wantErr: `"x"`},
		{name: "partial match", params: map[string]string{"name": "abc1"}, wantErr: `"name"`},
		{name: "leading dash", params: map[string]string{"name": "-a"}, wantErr: "leading"},
		{name: "too long", params: map[string]string{"name": strings.Repeat("a", 257)}, wantErr: "256"},
		{name: "NUL", params: map[string]string{"name": "a\x00b"}, wantErr: "NUL"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := a.Argv(tt.params)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("want error, got %v", got)
				}
				if !errors.Is(err, ErrInvalidParams) {
					t.Errorf("error %v does not wrap ErrInvalidParams", err)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("error %q does not mention %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestArgvMaxLength(t *testing.T) {
	defs := mustParse(t, action(`argv = ["echo", "{p}"]
[actions.a.params.p]
pattern = "a+"`))
	got, err := defs.Actions["a"].Argv(map[string]string{"p": strings.Repeat("a", 256)})
	if err != nil || len(got[1]) != 256 {
		t.Errorf("256-byte value: %v", err)
	}
}

func TestArgvAlternationAnchored(t *testing.T) {
	defs := mustParse(t, action(`argv = ["echo", "{p}"]
[actions.a.params.p]
pattern = "a|b"`))
	if _, err := defs.Actions["a"].Argv(map[string]string{"p": "ab"}); err == nil {
		t.Error(`"ab" matched "a|b"`)
	}
}

func TestArgvCoordinateFetch(t *testing.T) {
	defs := parseSpecExample(t)
	if _, err := defs.Actions["fetch"].Argv(nil); err == nil {
		t.Error("want error for coordinate-fetch")
	}
}

func TestAgentListing(t *testing.T) {
	defs := parseSpecExample(t)
	got, err := json.Marshal(defs.AgentListing())
	if err != nil {
		t.Fatal(err)
	}
	want := `[` +
		`{"name":"format","description":"Apply formatter to sources","params":{}},` +
		`{"name":"lint","description":"Frontend lint","params":{}},` +
		`{"name":"npm-ci","description":"Install node_modules from the project cache","params":{}},` +
		`{"name":"test","description":"Run backend tests, optionally one class","params":{` +
		`"test_class":{"pattern":"[A-Za-z0-9_.#*]+","optional":true,"allow_leading_dash":false,"description":"Surefire -Dtest filter"}}}` +
		`]`
	if string(got) != want {
		t.Errorf("listing\n got %s\nwant %s", got, want)
	}
}

func TestProtectedPaths(t *testing.T) {
	for _, p := range []string{".git", ".agent-actions.toml", ".gitattributes", ".mcp.json", "mvnw", ".npmrc", ".ripgreprc"} {
		if !IsProtectedName(p) {
			t.Errorf("%s not protected", p)
		}
	}
	if IsProtectedName("src") {
		t.Error("src protected")
	}
	if len(ProtectedPaths) != 20 {
		t.Errorf("len(ProtectedPaths) = %d, want 20", len(ProtectedPaths))
	}
}

func mustParse(t *testing.T, src string) *Definitions {
	t.Helper()
	defs, err := Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	return defs
}

func action(extra string) string {
	src := "schema_version = 1\n[actions.a]\ndescription = \"d\"\ninvoker = \"agent\"\n"
	if !strings.Contains(extra, "argv") {
		src += "argv = [\"true\"]\n"
	}
	return src + extra + "\n"
}

func writes(list string) string {
	return action("writes = [" + list + "]")
}
