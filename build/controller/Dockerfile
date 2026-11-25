ARG GO_VERSION=1.26.10

FROM golang:${GO_VERSION} AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
ARG COMMIT=unknown
RUN CGO_ENABLED=0 go build -trimpath \
      -ldflags "-s -w -X rondo/internal/version.Version=${VERSION} -X rondo/internal/version.Commit=${COMMIT}" \
      -o /out/rondo-controller ./cmd/rondo-controller

FROM gcr.io/distroless/static:nonroot
COPY --from=build /out/rondo-controller /usr/local/bin/rondo-controller
EXPOSE 8081
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/rondo-controller"]
