ARG GO_VERSION=1.26.8
FROM golang:${GO_VERSION}-alpine AS build

ARG GO_VERSION

WORKDIR /src

# Download module dependencies first so source edits do not invalidate the
# cached dependency layer. Only the module manifests and Go sources enter the
# build context (see .dockerignore); repository history, local environment
# files, and host credential variants are never sent to the builder.
COPY go.mod go.sum ./
RUN go mod download

COPY cmd/ ./cmd/
COPY internal/ ./internal/

RUN test "$(go env GOVERSION)" = "go${GO_VERSION}" \
    && CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/redlaunch ./cmd/redlaunch \
    && go version -m /out/redlaunch > /out/redlaunch.buildinfo

FROM alpine:3.22

ARG GO_VERSION

LABEL io.redlaunch.build.go-version="${GO_VERSION}"

RUN apk add --no-cache docker-cli docker-cli-compose dbus openssh-keygen

COPY --from=build /out/redlaunch /usr/local/bin/redlaunch
COPY --from=build /out/redlaunch.buildinfo /usr/local/share/redlaunch/buildinfo.txt

ENV HTTP_ADDR=:8080

EXPOSE 8080

ENTRYPOINT ["/usr/local/bin/redlaunch"]
