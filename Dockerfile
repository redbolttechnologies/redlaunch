FROM golang:1.25-alpine AS build

WORKDIR /src

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/redlaunch ./cmd/redlaunch

FROM alpine:3.22

RUN apk add --no-cache docker-cli docker-cli-compose dbus openssh-keygen

COPY --from=build /out/redlaunch /usr/local/bin/redlaunch

ENV HTTP_ADDR=:8080

EXPOSE 8080

ENTRYPOINT ["/usr/local/bin/redlaunch"]
