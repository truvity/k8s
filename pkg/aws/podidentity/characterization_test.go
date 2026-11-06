package podidentity_test

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/truvity/k8s/pkg/aws/podidentity"
)

var update = flag.Bool("update", false, "rewrite testdata/*.json")

// TestDefaultModeIsPinned pins, byte for byte, everything the default mode
// registers for the base() arguments: each child's type, name, parent,
// provider, protection, alias and import options and inputs, and so the
// default trust policy inside the role's inputs. A change that adds a mode
// must leave this document alone: a caller that sets no mode sees no diff,
// and a child's name is part of its URN (docs/decisions/0002-urn-stability.md).
//
// Regenerate with -update only for a change that is meant to alter the
// default mode, and say so as a Breaking entry in the CHANGELOG.
func TestDefaultModeIsPinned(t *testing.T) {
	adopted := base()
	adopted.ServiceAccounts = []string{"zeta", "alpha"}
	adopted.Protect = true
	adopted.IgnoreAssociationTags = true
	adopted.Imports = podidentity.Imports{
		Role: "c1-app", Policy: "policy-arn", Attachment: "c1-app/policy-arn",
		Associations: map[string]string{"zeta": "c1,a-zeta"},
	}

	for file, args := range map[string]*podidentity.Args{"default-mode": base(), "default-mode-adopted": adopted} {
		t.Run(file, func(t *testing.T) { pinned(t, file, args) })
	}
}

func pinned(t *testing.T, file string, args *podidentity.Args) {
	t.Helper()

	res, err := run(t, args)
	if err != nil {
		t.Fatal(err)
	}

	type child struct {
		Type     string         `json:"type"`
		Name     string         `json:"name"`
		Parent   string         `json:"parent"`
		Provider string         `json:"provider"`
		Protect  bool           `json:"protect,omitempty"`
		Import   string         `json:"import,omitempty"`
		Ignore   []string       `json:"ignoreChanges,omitempty"`
		Aliases  []alias        `json:"aliases,omitempty"`
		Inputs   map[string]any `json:"inputs"`
	}

	kids := children(res.regs)
	out := make([]child, 0, len(kids))

	for i := range kids {
		r := &kids[i]
		in := map[string]any{}

		for k, v := range r.inputs.Mappable() {
			in[k] = v
		}

		out = append(out, child{
			Type: r.typ, Name: r.name, Parent: urnTail(r.parent), Provider: urnTail(r.provider),
			Protect: r.protect, Import: r.importID, Ignore: r.ignore, Aliases: r.aliases, Inputs: in,
		})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Type+out[i].Name < out[j].Type+out[j].Name })

	raw, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		t.Fatal(err)
	}

	got := string(raw)

	path := filepath.Join("testdata", file+".json")
	if *update {
		if err := os.WriteFile(path, []byte(got+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	if got != strings.TrimSuffix(string(want), "\n") {
		t.Errorf("%s differs (run with -update only for a change meant to alter the default mode):\n got: %s\nwant: %s", path, got, want)
	}
}

// urnTail drops the stack and project from a URN, which the mock run fixes.
func urnTail(urn string) string {
	if _, tail, ok := strings.Cut(urn, "::"); ok {
		return tail
	}

	return urn
}
