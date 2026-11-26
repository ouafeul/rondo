ARG GO_VERSION=1.27.4
ARG HELM_VERSION=4.3.2

FROM alpine/helm:${HELM_VERSION} AS helm

FROM golang:${GO_VERSION} AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
ARG COMMIT=unknown
RUN CGO_ENABLED=0 go build -trimpath \
      -ldflags "-s -w -X rondo/internal/version.Version=${VERSION} -X rondo/internal/version.Commit=${COMMIT}" \
      -o /out/rondo ./cmd/rondo

FROM debian:13-slim
RUN apt-get update \
    && apt-get upgrade -y \
    && apt-get install -y --no-install-recommends ca-certificates skopeo python3 python3-requests python3-yaml \
    && rm -rf /var/lib/apt/lists/*
COPY --from=helm /usr/bin/helm /usr/local/bin/helm
COPY --from=build /out/rondo /usr/local/bin/rondo
COPY manifests/crds /opt/rondo/crds
# The deps train, for a component to mirror the upstream dependencies of its own manifest.
COPY scripts/ci/extract-helm-images.py scripts/ci/resolve-deps.py scripts/ci/download-deps.py \
     scripts/ci/mirror-images.py scripts/ci/mirror_common.py /opt/rondo/deps/
ENV RONDO_CRDS_DIR=/opt/rondo/crds
WORKDIR /work
