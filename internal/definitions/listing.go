package definitions

import (
	"maps"
	"slices"
)

type ListedAction struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Params      map[string]ListedParam `json:"params"`
}

type ListedParam struct {
	Pattern          string `json:"pattern"`
	Optional         bool   `json:"optional"`
	AllowLeadingDash bool   `json:"allow_leading_dash"`
	Description      string `json:"description"`
}

func (d *Definitions) AgentListing() []ListedAction {
	listing := []ListedAction{}
	for _, name := range d.Names() {
		a := d.Actions[name]
		if a.Invoker != InvokerAgent {
			continue
		}
		params := make(map[string]ListedParam, len(a.Params))
		for pname, p := range a.Params {
			params[pname] = ListedParam{
				Pattern:          p.Pattern,
				Optional:         p.Optional,
				AllowLeadingDash: p.AllowLeadingDash,
				Description:      p.Description,
			}
		}
		listing = append(listing, ListedAction{Name: a.Name, Description: a.Description, Params: params})
	}
	return listing
}

func sortedKeys[V any](m map[string]V) []string {
	return slices.Sorted(maps.Keys(m))
}
