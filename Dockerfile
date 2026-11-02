ARG GO_VERSION=1.26.10
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_DATE=unknown
FROM golang:${GO_VERSION}-alpine AS build

ARG GO_VERSION
ARG VERSION
ARG COMMIT
ARG BUILD_DATE

WORKDIR /src

# Download module dependencies first so source edits do not invalidate the
# cached dependency layer. Only the module manifests, Go sources, and public
# operator guides enter the build context (see .dockerignore). Repository
# history, local environment files, and host credential variants are never
# sent to the builder.
# The module cache mount below persists across builds on the same builder so
# the layer stays small and the compile step below can reuse downloads.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY cmd/ ./cmd/
COPY internal/ ./internal/
COPY documentation.go README.md INSTALL.md DEPENDENCIES.md RECIPES.md SSH_KEYS.md API_TOKENS.md MCP.md REFERENCE.md LICENSE ./

# The Go build cache mount keeps recompiles incremental across image builds:
# without it every build recompiles all dependencies (notably the large
# modernc.org SQLite tree) from scratch, which takes minutes on a small VPS.
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    test "$(go env GOVERSION)" = "go${GO_VERSION}" \
    && CGO_ENABLED=0 GOOS=linux go build -trimpath \
       -ldflags="-w -X redlaunch/internal/version.Version=${VERSION} -X redlaunch/internal/version.Commit=${COMMIT} -X redlaunch/internal/version.BuildDate=${BUILD_DATE}" \
       -o /out/redlaunch ./cmd/redlaunch \
    && go version -m /out/redlaunch > /out/redlaunch.buildinfo

FROM alpine:3.22

ARG GO_VERSION
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_DATE=unknown

LABEL io.redlaunch.build.go-version="${GO_VERSION}" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${COMMIT}" \
      org.opencontainers.image.created="${BUILD_DATE}" \
      io.redlaunch.deployment-contract="1"

RUN apk add --no-cache docker-cli docker-cli-compose dbus git openssh-keygen

COPY --from=build /out/redlaunch /usr/local/bin/redlaunch
COPY --from=build /out/redlaunch.buildinfo /usr/local/share/redlaunch/buildinfo.txt
COPY --from=build /src/LICENSE /usr/local/share/redlaunch/LICENSE

ENV HTTP_ADDR=:8080

EXPOSE 8080

ENTRYPOINT ["/usr/local/bin/redlaunch"]
