# Copyright (c) 2026 Jody Scott
# SPDX-License-Identifier: Apache-2.0
#
# Release image: the pre-built static doppelganger binary on a minimal base. The binary
# is built by the fleet's image pipeline against the fleet's merged protos.
ARG BASE_IMAGE=alpine:3.22
FROM ${BASE_IMAGE}
ARG VERSION=0.0.0
ARG VCS_REF=local
LABEL org.opencontainers.image.title="doppelganger" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${VCS_REF}" \
      org.opencontainers.image.licenses="Apache-2.0" \
      org.opencontainers.image.source="https://github.com/tacplus-dev/doppelganger"
RUN apk add --no-cache ca-certificates && adduser -D -u 65532 app
COPY bin/doppelganger /usr/local/bin/doppelganger
USER 65532
# Every service runs under the launcher (github.com/tacplus-dev/launcher): PID 1
# that forwards signals, propagates the exit code, and kills a deadlocked child
# on gRPC health. The fleet's image pipeline puts the binary beside the Dockerfile.
ARG TARGETARCH
COPY launcher-linux-${TARGETARCH} /usr/local/bin/launcher
ENV LAUNCH_CMD="doppelganger" HEALTH_PORT=8443
ENTRYPOINT ["launcher"]
