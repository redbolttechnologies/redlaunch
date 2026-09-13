ARG GO_VERSION=1.26.8
FROM golang:${GO_VERSION}-alpine AS build

ARG GO_VERSION

WORKDIR /src

# Download module dependencies first so source edits do not invalidate the
# cached dependency layer. Only the module manifests and Go sources enter the
# build context (see .dockerignore); repository history, local environment
# files, and host credential variants are never sent to the builder.
# The module cache mount below persists across builds on the same builder so
# the layer stays small and the compile step below can reuse downloads.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY cmd/ ./cmd/
COPY internal/ ./internal/

# The Go build cache mount keeps recompiles incremental across image builds:
# without it every build recompiles all dependencies (notably the large
# modernc.org SQLite tree) from scratch, which takes minutes on a small VPS.
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    test "$(go env GOVERSION)" = "go${GO_VERSION}" \
    && CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/redlaunch ./cmd/redlaunch \
    && go version -m /out/redlaunch > /out/redlaunch.buildinfo

FROM alpine:3.22

ARG GO_VERSION

LABEL io.redlaunch.build.go-version="${GO_VERSION}"

RUN apk add --no-cache docker-cli docker-cli-compose dbus git openssh-keygen

COPY --from=build /out/redlaunch /usr/local/bin/redlaunch
COPY --from=build /out/redlaunch.buildinfo /usr/local/share/redlaunch/buildinfo.txt

ENV HTTP_ADDR=:8080

EXPOSE 8080

ENTRYPOINT ["/usr/local/bin/redlaunch"]
