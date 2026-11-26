ARG GO_VERSION=1.27.4
ARG HELM_VERSION=4.3.2
ARG KUBECTL_VERSION=v1.36.6
ARG TOFU_VERSION=1.13.10
ARG ANSIBLE_CORE_VERSION=2.22.0

FROM alpine/helm:${HELM_VERSION} AS helm
FROM registry.k8s.io/kubectl:${KUBECTL_VERSION} AS kubectl

FROM debian:13-slim AS tofu
ARG TOFU_VERSION
ARG TARGETARCH
RUN apt-get update && apt-get install -y --no-install-recommends curl ca-certificates \
 && curl -fsSL "https://github.com/opentofu/opentofu/releases/download/v${TOFU_VERSION}/tofu_${TOFU_VERSION}_linux_${TARGETARCH}.tar.gz" \
    | tar xz -C /usr/local/bin tofu \
 && /usr/local/bin/tofu version

FROM golang:${GO_VERSION} AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
ARG COMMIT=unknown
RUN CGO_ENABLED=0 go build -trimpath \
      -ldflags "-s -w -X rondo/internal/version.Version=${VERSION} -X rondo/internal/version.Commit=${COMMIT}" \
      -o /out/rondo-toolbox ./cmd/rondo-toolbox

FROM debian:13-slim
ARG ANSIBLE_CORE_VERSION
RUN apt-get update \
 && apt-get upgrade -y \
 && apt-get install -y --no-install-recommends \
      git ca-certificates openssh-client python3 python3-pip \
 && pip3 install --no-cache-dir --break-system-packages \
      "ansible-core==${ANSIBLE_CORE_VERSION}" \
 && ansible-playbook --version \
 && apt-get purge -y python3-pip \
 && apt-get autoremove -y \
 && rm -rf /var/lib/apt/lists/*

COPY --from=helm    /usr/bin/helm            /usr/local/bin/helm
COPY --from=kubectl /bin/kubectl             /usr/local/bin/kubectl
COPY --from=tofu    /usr/local/bin/tofu      /usr/local/bin/tofu
COPY --from=build   /out/rondo-toolbox        /usr/local/bin/rondo-toolbox

RUN useradd --uid 65532 --create-home --home-dir /home/rondo --shell /usr/sbin/nologin rondo
USER 65532:65532
WORKDIR /work
ENTRYPOINT ["/usr/local/bin/rondo-toolbox"]
