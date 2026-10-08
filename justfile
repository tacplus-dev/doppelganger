set shell := ["bash", "-eu", "-o", "pipefail", "-c"]

default: build

# Regenerate the tacplus Go model (./model, git-ignored) from ./proto.
tacplus:
    tacplus build --tag latest

build:
    go build -o doppelganger ./cmd/doppelganger

test:
    go test ./internal/...

install:
    go install ./cmd/doppelganger

run-daemon: build
    ./doppelganger daemon
