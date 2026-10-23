package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"
)

// The chart and the command agree: every golden render's container
// arguments parse, and the chart's defaults are the command's.
func TestChartArgsParse(t *testing.T) {
	goldens, err := filepath.Glob("../../tests/golden/netpol-stuck-pod-repair/*.yaml")
	if err != nil || len(goldens) == 0 {
		t.Fatalf("no golden renders: %v", err)
	}
	for _, g := range goldens {
		t.Run(filepath.Base(g), func(t *testing.T) {
			args := deploymentArgs(t, g)
			o, err := parseFlags(args)
			if err != nil {
				t.Fatalf("the chart's arguments do not parse: %v\n%v", err, args)
			}
			if filepath.Base(g) == "minimal.yaml" {
				want := DefaultConfig()
				want.ExcludeNamespaces = []string{"kube-system"}
				if o.cfg.Mode != ModeReport {
					t.Errorf("chart default mode = %s, want report", o.cfg.Mode)
				}
				if o.cfg.MaxRepairs != want.MaxRepairs || o.cfg.OwnerCooldown != want.OwnerCooldown ||
					o.cfg.MinDeleteInterval != want.MinDeleteInterval || o.cfg.HaltWindow != want.HaltWindow ||
					o.cfg.Interval != time.Minute || o.cfg.MaxNodeAge != 0 || o.cfg.NodeDiagnostic {
					t.Errorf("chart defaults differ from the command's: %+v", o.cfg)
				}
				if len(o.cfg.TimeoutPatterns) != 2 || o.cfg.TimeoutPatterns[1] != "connect: connection timed out" {
					t.Errorf("timeout patterns = %q", o.cfg.TimeoutPatterns)
				}
			}
		})
	}
}

func deploymentArgs(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	dec := yaml.NewDecoder(bytes.NewReader(b))
	for {
		var doc struct {
			Kind string `yaml:"kind"`
			Spec struct {
				Template struct {
					Spec struct {
						Containers []struct {
							Args []string `yaml:"args"`
						} `yaml:"containers"`
					} `yaml:"spec"`
				} `yaml:"template"`
			} `yaml:"spec"`
		}
		if err := dec.Decode(&doc); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			t.Fatal(err)
		}
		if doc.Kind == "Deployment" {
			return doc.Spec.Template.Spec.Containers[0].Args
		}
	}
	t.Fatalf("no Deployment in %s", path)
	return nil
}
