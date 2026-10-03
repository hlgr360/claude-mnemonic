package projects

import (
	"fmt"
	"strings"
	"time"
)

// Info is what labelling needs to know about a real project.
type Info struct {
	ID              string
	SampleTitle     string
	Observations    int64
	LastActiveEpoch int64 // milliseconds
}

// Label tells a client how to show a project and what to pass back to refer to it.
type Label struct {
	// Label is for people: the plain name when it is unique, otherwise the name
	// plus what tells the projects apart.
	Label string `json:"label"`
	// Use is for the next call: the name when it is unique, otherwise the id.
	Use string `json:"use"`
}

// Detail describes a project by what distinguishes it from a namesake:
// how much it holds, when it was last used, and a sample of its content.
func Detail(i Info) string {
	parts := []string{plural(i.Observations, "observation")}
	if i.LastActiveEpoch > 0 {
		parts = append(parts, "last used "+time.UnixMilli(i.LastActiveEpoch).Format("2006-01-02"))
	}
	if t := strings.TrimSpace(i.SampleTitle); t != "" {
		parts = append(parts, fmt.Sprintf("e.g. %q", t))
	}
	return strings.Join(parts, ", ")
}

func plural(n int64, word string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", word)
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// Labels works out, for every project, how to show it and how to refer to it.
//
// A name that identifies exactly one project is used as is. When several
// projects share a name (two folders called "app" in different places) each
// is labelled with what distinguishes it and must be referred to by id.
// Names are compared case-insensitively, matching how names are resolved.
// Ids that resolve to the same project through an alias are one project, not
// namesakes, and share its label.
func Labels(infos []Info, aliases map[string]string) map[string]Label {
	canonical := func(id string) string {
		if c, ok := aliases[id]; ok {
			return c
		}
		return id
	}

	byID := make(map[string]Info, len(infos))
	for _, in := range infos {
		byID[in.ID] = in
	}

	// Distinct real projects per lower-cased name.
	groups := map[string]map[string]struct{}{}
	for _, in := range infos {
		id := canonical(in.ID)
		key := strings.ToLower(DisplayName(id))
		if groups[key] == nil {
			groups[key] = map[string]struct{}{}
		}
		groups[key][id] = struct{}{}
	}

	out := make(map[string]Label, len(infos))
	for _, in := range infos {
		id := canonical(in.ID)
		subject, ok := byID[id]
		if !ok {
			subject = in // the project an alias points at has no data of its own listed
		}
		name := DisplayName(id)
		if len(groups[strings.ToLower(name)]) == 1 {
			out[in.ID] = Label{Label: name, Use: name}
			continue
		}
		out[in.ID] = Label{Label: fmt.Sprintf("%s (%s)", name, Detail(subject)), Use: id}
	}
	return out
}
