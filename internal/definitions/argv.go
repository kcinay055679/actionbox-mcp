package definitions

import (
	"errors"
	"fmt"
	"strings"
)

var ErrInvalidParams = errors.New("invalid params")

const maxParamBytes = 256

func (a *Action) Argv(params map[string]string) ([]string, error) {
	if a.Kind != KindRun {
		return nil, fmt.Errorf("action %q: kind %q has no argv", a.Name, a.Kind)
	}
	if err := a.validateParams(params); err != nil {
		return nil, fmt.Errorf("%w: action %q: %v", ErrInvalidParams, a.Name, err)
	}
	argv := make([]string, 0, len(a.ArgvTemplate))
	for _, elem := range a.ArgvTemplate {
		if out, ok := substitute(elem, params); ok {
			argv = append(argv, out)
		}
	}
	return argv, nil
}

// single pass: substituted values never re-expanded
func substitute(elem string, params map[string]string) (string, bool) {
	ok := true
	out := placeholder.ReplaceAllStringFunc(elem, func(m string) string {
		value, present := params[m[1:len(m)-1]]
		ok = ok && present
		return value
	})
	return out, ok
}

func (a *Action) validateParams(values map[string]string) error {
	for name := range values {
		if _, ok := a.Params[name]; !ok {
			return fmt.Errorf("unknown param %q", name)
		}
	}
	for _, name := range sortedKeys(a.Params) {
		p := a.Params[name]
		value, present := values[name]
		if !present {
			if !p.Optional {
				return fmt.Errorf("param %q is required", name)
			}
			continue
		}
		if err := p.validate(value); err != nil {
			return fmt.Errorf("param %q: %v", name, err)
		}
	}
	return nil
}

func (p Param) validate(value string) error {
	switch {
	case strings.ContainsRune(value, 0):
		return errors.New("value contains NUL")
	case len(value) > maxParamBytes:
		return fmt.Errorf("value longer than %d bytes", maxParamBytes)
	case strings.HasPrefix(value, "-") && !p.AllowLeadingDash:
		return errors.New("value has leading '-' and allow_leading_dash is not set")
	case !p.re.MatchString(value):
		return fmt.Errorf("value does not match pattern %q", p.Pattern)
	}
	return nil
}
