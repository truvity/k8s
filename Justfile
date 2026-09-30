# Development commands. Everything CI runs is a recipe here — the shared
# check workflow (truvity/ci-workflows) runs each one as its own job.

# Format Go files.
fmt:
    golangci-lint fmt ./...

# Compile everything.
build:
    go build ./...

# Run the tests. They never reach a cloud: providers are exercised against
# Pulumi mocks, and cluster behaviour against kind.
test:
    go test ./...

# Run linters. `config verify` first: `run` accepts unknown top-level
# keys silently.
lint:
    golangci-lint config verify
    golangci-lint run ./...

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
