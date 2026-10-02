# Development commands. Everything CI runs is a recipe here — the shared
# check workflow (truvity/ci-workflows) runs each one as its own job.

charts := "cluster-baseline"
crd-charts := "volume-snapshot-crds cilium-crds"

# Format Go files.
fmt:
    golangci-lint fmt ./...

# Compile everything.
build:
    go build ./...

# Run the tests. They never reach a cloud: providers are exercised against
# Pulumi mocks, and cluster behaviour against kind. The charts are rendered
# and compared with their goldens (`hack/golden.sh`); no cluster is involved.
test:
    go test ./...
    hack/golden.sh

# Run linters. `config verify` first: `run` accepts unknown top-level
# keys silently. Then every chart: `helm lint`, an unknown key must fail the
# render, and every negative fixture under tests/invalid/<chart>/ must fail
# too (one per refusal, so a loosened schema or a dropped `fail` is caught).
lint:
    #!/usr/bin/env bash
    set -euo pipefail
    golangci-lint config verify
    golangci-lint run ./...
    for chart in {{ charts }}; do
      helm lint "charts/$chart"
      # Not `! helm template ...`: bash's `set -e` ignores a negated
      # command, so such a probe could never fail the recipe.
      if helm template x "charts/$chart" --set bogusKey=1 >/dev/null 2>&1; then
        echo "$chart: an unknown key rendered" >&2
        exit 1
      fi
      for values in tests/invalid/"$chart"/*.yaml; do
        if helm template invalid "charts/$chart" -f "$values" >/dev/null 2>&1; then
          echo "RENDERED BUT SHOULD HAVE FAILED: $values" >&2
          exit 1
        fi
      done
      echo "$chart: schema and $(ls tests/invalid/"$chart"/*.yaml | wc -l | tr -d ' ') negative fixtures OK"
    done

# The CRD mirror charts ({{ crd-charts }}): upstream CRDs vendored verbatim
# as charts/<chart>/templates/crds.yaml, generated from charts/<chart>/crdctl.yaml
# by crdctl (truvity/ocictl, pinned below). The release workflow packages
# the chart directory as it stands, so the generated file is COMMITTED;
# nothing is fetched at release time. `just crds` regenerates it after a
# pinned_version bump; review the diff before committing.
crdctl := "github.com/truvity/ocictl/cmd/crdctl@v0.7.1"

crds:
    #!/usr/bin/env bash
    set -euo pipefail
    export GOWORK=off
    # crdctl reads the upstream repository through the GitHub API; CI hands the
    # job token over as GITHUB_PACKAGES_TOKEN, which lifts the anonymous rate limit.
    export GITHUB_TOKEN="${GITHUB_TOKEN:-${GITHUB_PACKAGES_TOKEN:-}}"
    for chart in {{ crd-charts }}; do
      go run {{ crdctl }} build --config "charts/$chart/crdctl.yaml"
    done

# The vendored CRDs still equal what crdctl produces from the pinned upstream
# version: a hand edit or a pin bumped without `just crds` fails here.
crds-check: crds
    git diff --exit-code -- 'charts/*/templates/crds.yaml'

# Lint the CRD mirror charts: they take no values, so any key must be refused
# (values.schema.json; one negative fixture per chart under tests/invalid/), and
# the render must contain CRDs only.
crd-charts-lint:
    #!/usr/bin/env bash
    set -euo pipefail
    for chart in {{ crd-charts }}; do
      helm lint "charts/$chart"
      if helm template x "charts/$chart" --set bogusKey=1 >/dev/null 2>&1; then
        echo "$chart: an unknown key rendered" >&2
        exit 1
      fi
      for values in tests/invalid/"$chart"/*.yaml; do
        if helm template invalid "charts/$chart" -f "$values" >/dev/null 2>&1; then
          echo "RENDERED BUT SHOULD HAVE FAILED: $values" >&2
          exit 1
        fi
      done
      kinds="$(helm template x "charts/$chart" | grep -E '^kind:' | sort -u)"
      if [ "$kinds" != "kind: CustomResourceDefinition" ]; then
        echo "$chart: renders more than CustomResourceDefinitions:" >&2
        echo "$kinds" >&2
        exit 1
      fi
      echo "$chart: lint, schema and CRD-only render OK"
    done

# Regenerate the golden chart renders; review the diff before committing.
golden:
    hack/golden.sh update

# Package every chart locally (the release workflow stamps the version from the tag).
package:
    #!/usr/bin/env bash
    set -euo pipefail
    for chart in {{ charts }} {{ crd-charts }}; do helm package "charts/$chart" --destination dist/; done

# Reachable Go advisories (security.yaml, daily). NOT part of `check`: a
# standard-library advisory with no released fix would turn every pull
# request red on a finding nobody can act on.
vuln:
    govulncheck ./...

# The reason this repository can be public. Runs in CI as its own job.
leak-canary:
    hack/leak-canary.sh

# Run go mod tidy.
tidy:
    go mod tidy

# Clean build artifacts.
clean:
    rm -rf bin/ dist/ coverage.out

# Everything CI runs on a pull request.
check: build test lint crd-charts-lint crds-check leak-canary
