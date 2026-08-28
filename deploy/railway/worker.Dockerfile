# See api.Dockerfile's header comment for why this is a separate file
# instead of a shared ARG SERVICE build. worker doesn't read
# shards.yaml/tiers.yaml (it publishes one physical shard's outbox, not
# aware of virtual-shard routing or tiers), so it skips that COPY.
FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -o /out/service ./cmd/worker

FROM alpine:3.20
RUN apk add --no-cache ca-certificates
COPY --from=build /out/service /usr/local/bin/service
ENTRYPOINT ["/usr/local/bin/service"]
