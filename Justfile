# Development commands. Everything CI runs is a recipe here — the shared
# check workflow (truvity/ci-workflows) runs each one as its own job.

charts := "cluster-baseline"

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

# Regenerate the golden chart renders; review the diff before committing.
golden:
    hack/golden.sh update

# Package every chart locally (the release workflow stamps the version from the tag).
package:
    #!/usr/bin/env bash
    set -euo pipefail
    for chart in {{ charts }}; do helm package "charts/$chart" --destination dist/; done

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
check: build test lint leak-canary
