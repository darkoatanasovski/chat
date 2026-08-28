# See api.Dockerfile's header comment for why this is a separate file
# instead of a shared ARG SERVICE build.
FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -o /out/service ./cmd/gateway

FROM alpine:3.20
RUN apk add --no-cache ca-certificates
COPY --from=build /out/service /usr/local/bin/service
COPY deploy/shards.yaml /etc/chat/shards.yaml
COPY deploy/tiers.yaml /etc/chat/tiers.yaml
ENTRYPOINT ["/usr/local/bin/service"]
