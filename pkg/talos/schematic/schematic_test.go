package schematic_test

import (
	"strings"
	"testing"

	"github.com/truvity/k8s/pkg/talos/schematic"
)

// The vectors are the Image Factory's own test vectors: if these hold, an ID
// computed here is the ID the factory serves.
func TestIDMatchesTheImageFactory(t *testing.T) {
	//nolint:lll // the vectors are copied verbatim
	for name, tc := range map[string]struct {
		yaml, want string
	}{
		"empty":            {`{}`, "376567988ad370138ad8b2698212367b8edcb69b5fd68c80be1f2ec7d603b4ba"},
		"empty custom":     {`customization: {}`, "376567988ad370138ad8b2698212367b8edcb69b5fd68c80be1f2ec7d603b4ba"},
		"empty args":       {`customization: {"extraKernelArgs": []}`, "376567988ad370138ad8b2698212367b8edcb69b5fd68c80be1f2ec7d603b4ba"},
		"kernel args":      {`{"customization": {"extraKernelArgs": ["noapic", "nolapic"], "systemExtensions": {}}}`, "9cba8e32753f91a16c1837ab8abf356af021706ef284aef07380780177d9a06c"},
		"meta":             {`{"customization": {"meta": [{"key": 10, "value": "foo"}]}}`, "d308a2a5ee2277bed5fbaa104fcbc8d59122abfa737df987a95b4ca763459a7f"},
		"overlay":          {`{"overlay": {"name": "rpi_generic", "image": "siderolabs/sbc-raspberrypi"},"customization":{}}`, "ee21ef4a5ef808a9b7484cc0dda0f25075021691c8c09a276591eedb638ea1f9"},
		"secureboot":       {`{"customization":{"secureboot": {"includeWellKnownCertificates": true}}}`, "fa8e05f142a851d3ee568eb0a8e5841eaf6b0ebc8df9a63df16ac5ed2c04f3e6"},
		"4k sector images": {`{"customization":{"diskImage": {"sectorSize": 4096}}}`, "92833e9ddd9bb9e11b2464fa5525429f01866306d52ebd882ee08d7918f6d1ea"},
	} {
		t.Run(name, func(t *testing.T) {
			s, err := schematic.Parse([]byte(tc.yaml))
			if err != nil {
				t.Fatal(err)
			}

			got, err := s.ID()
			if err != nil {
				t.Fatal(err)
			}

			if got != tc.want {
				t.Errorf("ID = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestParseRefusesAnUnknownField(t *testing.T) {
	if _, err := schematic.Parse([]byte("customization:\n  systemExtension:\n    officialExtensions: [siderolabs/iscsi-tools]\n")); err == nil {
		t.Error("a misspelt field parsed")
	}
}

func TestReference(t *testing.T) {
	id := strings.Repeat("ab", 32)

	for name, tc := range map[string]struct {
		in   schematic.Installer
		want string
	}{
		"defaults":   {schematic.Installer{SchematicID: id}, "factory.talos.dev/metal-installer/" + id + ":v1.14.2"},
		"secureboot": {schematic.Installer{SchematicID: id, SecureBoot: true}, "factory.talos.dev/metal-installer-secureboot/" + id + ":v1.14.2"},
		"mirror": {
			schematic.Installer{SchematicID: id, Factory: "factory.example.com", Platform: "nocloud"},
			"factory.example.com/nocloud-installer/" + id + ":v1.14.2",
		},
		"own image": {schematic.Installer{Image: "registry.example.com/boards/installer"}, "registry.example.com/boards/installer:v1.14.2"},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := tc.in.Reference("v1.14.2")
			if err != nil || got != tc.want {
				t.Errorf("Reference = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestReferenceRefusals(t *testing.T) {
	id := strings.Repeat("ab", 32)

	for name, tc := range map[string]struct {
		in      schematic.Installer
		version string
		want    string
	}{
		"no image":     {schematic.Installer{}, "v1.14.2", "not a 64-hex"},
		"short id":     {schematic.Installer{SchematicID: "abc"}, "v1.14.2", "not a 64-hex"},
		"both":         {schematic.Installer{SchematicID: id, Image: "r.example.com/i"}, "v1.14.2", "exclusive"},
		"tagged image": {schematic.Installer{Image: "r.example.com/i:v1"}, "v1.14.2", "carries a tag"},
		"factory url":  {schematic.Installer{SchematicID: id, Factory: "https://factory.example.com"}, "v1.14.2", "is a host"},
		"version":      {schematic.Installer{SchematicID: id}, "1.14.2", "not vX.Y.Z"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := tc.in.Reference(tc.version); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}
