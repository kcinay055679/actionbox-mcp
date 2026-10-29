package definitions

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/BurntSushi/toml"

	"actionbox/internal/limits"
)

var ErrInvalid = errors.New("invalid definitions")

const SchemaVersion = 1

type Invoker string

const (
	InvokerAgent Invoker = "agent"
	InvokerOwner Invoker = "owner"
)

type Kind string

const (
	KindRun             Kind = "run"
	KindCoordinateFetch Kind = "coordinate-fetch"
)

type Definitions struct {
	Actions map[string]*Action
}

type Action struct {
	Name         string
	Description  string
	Invoker      Invoker
	Kind         Kind
	Containers   bool
	ArgvTemplate []string
	Writes       []string
	Limits       limits.Limits
	Params       map[string]Param
}

type Param struct {
	Name             string
	Pattern          string
	Optional         bool
	AllowLeadingDash bool
	Description      string
	re               *regexp.Regexp
}

type rawFile struct {
	SchemaVersion *int                 `toml:"schema_version"`
	Actions       map[string]rawAction `toml:"actions"`
}

type rawAction struct {
	Description string              `toml:"description"`
	Invoker     string              `toml:"invoker"`
	Kind        string              `toml:"kind"`
	Containers  bool                `toml:"containers"`
	Argv        []string            `toml:"argv"`
	Writes      []string            `toml:"writes"`
	Params      map[string]rawParam `toml:"params"`
	limits.Spec
}

type rawParam struct {
	Pattern          string `toml:"pattern"`
	Optional         bool   `toml:"optional"`
	AllowLeadingDash bool   `toml:"allow_leading_dash"`
	Description      string `toml:"description"`
}

var (
	actionNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
	paramNamePattern  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	placeholder       = regexp.MustCompile(`\{([A-Za-z_][A-Za-z0-9_]*)\}`)
)

func Parse(data []byte) (*Definitions, error) {
	var raw rawFile
	md, err := toml.Decode(string(data), &raw)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		return nil, fmt.Errorf("%w: unknown key %s", ErrInvalid, undecoded[0])
	}
	if raw.SchemaVersion == nil || *raw.SchemaVersion != SchemaVersion {
		return nil, fmt.Errorf("%w: schema_version must be %d", ErrInvalid, SchemaVersion)
	}

	defs := &Definitions{Actions: make(map[string]*Action, len(raw.Actions))}
	for _, name := range sortedKeys(raw.Actions) {
		action, err := parseAction(name, raw.Actions[name], md.IsDefined("actions", name, "argv"))
		if err != nil {
			return nil, fmt.Errorf("%w: action %q: %v", ErrInvalid, name, err)
		}
		defs.Actions[name] = action
	}
	return defs, nil
}

func (d *Definitions) Names() []string {
	return sortedKeys(d.Actions)
}

func parseAction(name string, ra rawAction, hasArgv bool) (*Action, error) {
	if !actionNamePattern.MatchString(name) {
		return nil, fmt.Errorf("name must match %s", actionNamePattern)
	}
	if ra.Description == "" {
		return nil, errors.New("description is required")
	}
	a := &Action{
		Name:         name,
		Description:  ra.Description,
		Invoker:      Invoker(ra.Invoker),
		Kind:         Kind(ra.Kind),
		Containers:   ra.Containers,
		ArgvTemplate: ra.Argv,
		Writes:       ra.Writes,
	}
	if a.Kind == "" {
		a.Kind = KindRun
	}
	if a.Invoker != InvokerAgent && a.Invoker != InvokerOwner {
		return nil, fmt.Errorf("invoker must be %q or %q, got %q", InvokerAgent, InvokerOwner, ra.Invoker)
	}

	var err error
	if a.Limits, err = ra.Spec.Parse(); err != nil {
		return nil, err
	}

	switch a.Kind {
	case KindRun:
		err = validateRun(a, ra, hasArgv)
	case KindCoordinateFetch:
		err = validateCoordinateFetch(a, ra, hasArgv)
	default:
		err = fmt.Errorf("kind must be %q or %q, got %q", KindRun, KindCoordinateFetch, ra.Kind)
	}
	return a, err
}

func validateCoordinateFetch(a *Action, ra rawAction, hasArgv bool) error {
	switch {
	case a.Invoker != InvokerOwner:
		return fmt.Errorf("invoker must be %q for kind %q", InvokerOwner, a.Kind)
	case hasArgv:
		return fmt.Errorf("argv not allowed for kind %q", a.Kind)
	case len(ra.Writes) > 0:
		return fmt.Errorf("writes not allowed for kind %q", a.Kind)
	case len(ra.Params) > 0:
		return fmt.Errorf("params not allowed for kind %q", a.Kind)
	case a.Containers:
		return fmt.Errorf("containers not allowed for kind %q", a.Kind)
	}
	return nil
}

func validateRun(a *Action, ra rawAction, hasArgv bool) error {
	if !hasArgv || len(a.ArgvTemplate) == 0 {
		return errors.New("argv must be a non-empty array")
	}
	if a.ArgvTemplate[0] == "" {
		return errors.New("argv[0] must not be empty")
	}
	for i, elem := range a.ArgvTemplate {
		if strings.ContainsRune(elem, 0) {
			return fmt.Errorf("argv[%d] contains NUL", i)
		}
	}
	params, err := parseParams(ra.Params)
	if err != nil {
		return err
	}
	if err := checkPlaceholders(a.ArgvTemplate, params); err != nil {
		return err
	}
	a.Params = params
	return validateWrites(a.Writes)
}

func parseParams(raw map[string]rawParam) (map[string]Param, error) {
	params := make(map[string]Param, len(raw))
	for _, name := range sortedKeys(raw) {
		rp := raw[name]
		if !paramNamePattern.MatchString(name) {
			return nil, fmt.Errorf("param %q: name must match %s", name, paramNamePattern)
		}
		if rp.Pattern == "" {
			return nil, fmt.Errorf("param %q: pattern is required", name)
		}
		re, err := regexp.Compile("^(?:" + rp.Pattern + ")$")
		if err != nil {
			return nil, fmt.Errorf("param %q: pattern: %v", name, err)
		}
		params[name] = Param{
			Name:             name,
			Pattern:          rp.Pattern,
			Optional:         rp.Optional,
			AllowLeadingDash: rp.AllowLeadingDash,
			Description:      rp.Description,
			re:               re,
		}
	}
	return params, nil
}

func checkPlaceholders(argv []string, params map[string]Param) error {
	used := make(map[string]bool, len(params))
	for i, elem := range argv {
		for _, m := range placeholder.FindAllStringSubmatch(elem, -1) {
			if _, ok := params[m[1]]; !ok {
				return fmt.Errorf("argv[%d]: placeholder {%s} has no declared param", i, m[1])
			}
			used[m[1]] = true
		}
	}
	for name := range params {
		if !used[name] {
			return fmt.Errorf("param %q is not used in argv", name)
		}
	}
	return nil
}
