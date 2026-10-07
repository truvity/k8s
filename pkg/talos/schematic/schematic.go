// Package schematic models a Talos Image Factory schematic: the overlay,
// kernel arguments and system extensions an installer image is built with.
// Its ID is the SHA-256 of the schematic's YAML, computed here offline the
// same way the Image Factory computes it, so a pinned ID can be checked
// against the schematic it claims to be without reaching the factory.
//
// A cluster pins the ID, not the schematic: the installer image reference
// (InstallerImage) carries the ID and the Talos version, and the factory
// serves that exact image for as long as it exists. Re-deriving the ID from
// the committed schematic in a test catches the classic drift where someone
// edits the schematic (adds an extension) but keeps the old ID, so every
// upgrade silently installs the old image.
package schematic

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/siderolabs/talos/pkg/machinery/imager/imageropts"
	"go.yaml.in/yaml/v4"
)

// DefaultFactory is the public Talos Image Factory.
const DefaultFactory = "factory.talos.dev"

// Schematic mirrors the Image Factory's schematic document. The field order
// and the YAML tags are part of the ID: they match the factory's own type.
type Schematic struct {
	Overlay       Overlay       `yaml:"overlay,omitempty"`
	Customization Customization `yaml:"customization"`
}

// Customization is what the schematic adds to the stock image.
type Customization struct {
	ExtraKernelArgs  []string                  `yaml:"extraKernelArgs,omitempty"`
	Meta             []MetaValue               `yaml:"meta,omitempty"`
	SystemExtensions SystemExtensions          `yaml:"systemExtensions,omitempty"`
	Bootloader       imageropts.BootloaderKind `yaml:"bootloader,omitempty"`
	SecureBoot       SecureBoot                `yaml:"secureboot,omitempty"`
	DiskImage        DiskImage                 `yaml:"diskImage,omitempty"`
}

// MetaValue is one META partition key and its value.
type MetaValue struct {
	Key   uint8  `yaml:"key"`
	Value string `yaml:"value"`
}

// SystemExtensions lists the official extensions by image name, for example
// "siderolabs/iscsi-tools".
type SystemExtensions struct {
	OfficialExtensions []string `yaml:"officialExtensions,omitempty"`
}

// Overlay is a board overlay (single-board computers): its image and name.
type Overlay struct {
	Image   string         `yaml:"image"`
	Name    string         `yaml:"name"`
	Options map[string]any `yaml:"options,omitempty"`
}

// SecureBoot customises a SecureBoot image.
type SecureBoot struct {
	IncludeWellKnownCertificates bool `yaml:"includeWellKnownCertificates,omitempty"`
}

// DiskImage customises the disk image.
type DiskImage struct {
	SectorSize uint `yaml:"sectorSize,omitempty"`
}

// Parse reads a schematic, refusing an unknown field: a typo would otherwise
// change nothing and the ID would silently be the stock image's.
func Parse(data []byte) (*Schematic, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)

	var s Schematic
	if err := dec.Decode(&s); err != nil {
		return nil, fmt.Errorf("schematic: %w", err)
	}

	return &s, nil
}

// Marshal is the schematic's canonical YAML, the bytes its ID hashes.
func (s *Schematic) Marshal() ([]byte, error) { return yaml.Marshal(s) }

// ID is the schematic's Image Factory ID: the hex SHA-256 of Marshal.
func (s *Schematic) ID() (string, error) {
	data, err := s.Marshal()
	if err != nil {
		return "", err
	}

	sum := sha256.Sum256(data)

	return hex.EncodeToString(sum[:]), nil
}

var idPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// ValidID reports whether id has the shape of a schematic ID.
func ValidID(id string) bool { return idPattern.MatchString(id) }

var versionPattern = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$`)

// Installer names an installer image built from a schematic.
type Installer struct {
	// Factory is the Image Factory host. Empty means DefaultFactory. A
	// pull-through mirror of the factory is configured as a registry mirror
	// in the machine config, not here: the reference stays the factory's.
	Factory string `json:"factory,omitempty" yaml:"factory,omitempty"`
	// Platform is the installer's platform, "metal" unless the nodes run on
	// a cloud or hypervisor image ("aws", "nocloud", ...).
	Platform string `json:"platform,omitempty" yaml:"platform,omitempty"`
	// SecureBoot selects the SecureBoot installer.
	SecureBoot bool `json:"secureBoot,omitempty" yaml:"secureBoot,omitempty"`
	// SchematicID is the 64-hex schematic ID. Required unless Image is set.
	SchematicID string `json:"schematicID,omitempty" yaml:"schematicID,omitempty"`
	// Image replaces the factory reference entirely, without its tag: for
	// a board the factory cannot build, whose installer comes from the
	// caller's own pipeline. The Talos version is appended as the tag.
	Image string `json:"image,omitempty" yaml:"image,omitempty"`
}

// Validate refuses an installer that names no image or two.
func (i Installer) Validate() error {
	var errs []error

	switch {
	case i.Image != "" && i.SchematicID != "":
		errs = append(errs, errors.New("installer: Image and SchematicID are exclusive"))
	case i.Image == "" && !ValidID(i.SchematicID):
		errs = append(errs, fmt.Errorf("installer: SchematicID %q is not a 64-hex schematic ID", i.SchematicID))
	case i.Image != "" && (strings.Contains(i.Image, "@") || strings.Contains(lastSegment(i.Image), ":")):
		errs = append(errs, fmt.Errorf("installer: Image %q carries a tag or digest; the Talos version is the tag", i.Image))
	}

	if i.Factory != "" && strings.Contains(i.Factory, "/") {
		errs = append(errs, fmt.Errorf("installer: Factory %q is a host, not a URL or path", i.Factory))
	}

	return errors.Join(errs...)
}

func lastSegment(ref string) string {
	if n := strings.LastIndex(ref, "/"); n >= 0 {
		return ref[n+1:]
	}

	return ref
}

// Reference is the installer image reference for a Talos version, for
// example factory.talos.dev/metal-installer/<id>:v1.14.2.
func (i Installer) Reference(talosVersion string) (string, error) {
	if err := i.Validate(); err != nil {
		return "", err
	}

	if !versionPattern.MatchString(talosVersion) {
		return "", fmt.Errorf("installer: Talos version %q is not vX.Y.Z", talosVersion)
	}

	if i.Image != "" {
		return i.Image + ":" + talosVersion, nil
	}

	factory, platform := i.Factory, i.Platform
	if factory == "" {
		factory = DefaultFactory
	}

	if platform == "" {
		platform = "metal"
	}

	kind := "installer"
	if i.SecureBoot {
		kind = "installer-secureboot"
	}

	return fmt.Sprintf("%s/%s-%s/%s:%s", factory, platform, kind, i.SchematicID, talosVersion), nil
}
