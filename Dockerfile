# syntax=docker/dockerfile:1

# The image is a provider registry for obot, meant to be mounted as an image volume. It holds the
# provider manifest and the static provider binary, at the paths obot expects relative to the
# registry root, and start-obot, the container entrypoint that adds the registry to obot's.

FROM --platform=$BUILDPLATFORM docker.io/library/golang:1.27-alpine@sha256:d200d2dd76b03a99b52651fd1325aff7dcd35422c35c01bac56571b0507f3deb AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/root/go/pkg/mod go mod download
COPY . .
ARG TARGETOS TARGETARCH VERSION=dev
RUN --mount=type=cache,target=/root/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w -buildid= -X main.version=${VERSION}" \
    -o /registry/authentik-auth-provider/bin/obot-provider . && \
    go build -trimpath -ldflags="-s -w -buildid=" -o /registry/start-obot ./cmd/start-obot
COPY registry/ /registry/

FROM scratch
COPY --from=build /registry/ /
