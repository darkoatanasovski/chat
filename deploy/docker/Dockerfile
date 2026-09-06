# Single platform image. One build, one binary; the role (api/ws/worker/
# router/og) is chosen by the container's command — see cmd/chat and
# docs/adr/0006-cell-based-tenant-routing.md. This replaces the old
# per-service ARG SERVICE build (there is only one binary now).
FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
# migrations is a Go package (migrations/embed.go) that go:embeds the *.sql
# files; the api applies them on startup, so it must be in the build context.
COPY migrations ./migrations
RUN CGO_ENABLED=0 go build -o /out/chat ./cmd/chat

FROM alpine:3.20
RUN apk add --no-cache ca-certificates
COPY --from=build /out/chat /usr/local/bin/chat
COPY deploy/tiers.yaml /etc/chat/tiers.yaml
COPY infra/topology.yaml /etc/chat/topology.yaml
# Default to the api role; docker-compose / Railway override with the role
# this particular instance should run (chat ws, chat worker, chat router, …).
ENTRYPOINT ["chat"]
CMD ["api"]
