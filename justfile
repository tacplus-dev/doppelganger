set shell := ["bash", "-eu", "-o", "pipefail", "-c"]

default: build

# Regenerate the tacplus Go model (./tacplus, git-ignored) from ./proto.
tacplus:
    tacplus build --tag latest

build:
    go build -o doppelganger ./cmd/doppelganger

test:
    go test ./internal/...

# What the looper (and a person before pushing) runs: generate, then format, vet, build, test.
gates: tacplus
    test -z "$(gofmt -l cmd internal)"
    go vet ./cmd/... ./internal/...
    go build ./cmd/... ./internal/...
    go test ./internal/...

install:
    go install ./cmd/doppelganger

run-daemon: build
    ./doppelganger daemon

# Run the daemon under launchd (starts at login, restarts if it dies).
install-agent: install
    cp dist/com.seabeam.doppelganger.plist ~/Library/LaunchAgents/
    launchctl bootout gui/$(id -u)/com.seabeam.doppelganger 2>/dev/null || true
    launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/com.seabeam.doppelganger.plist

uninstall-agent:
    launchctl bootout gui/$(id -u)/com.seabeam.doppelganger 2>/dev/null || true
    rm -f ~/Library/LaunchAgents/com.seabeam.doppelganger.plist
