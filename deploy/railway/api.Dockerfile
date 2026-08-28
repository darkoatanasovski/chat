# Railway builds one Dockerfile per service and (as far as this setup can
# rely on) doesn't expose a way to pass a custom --build-arg through its
# config, unlike deploy/docker/Dockerfile's ARG SERVICE used for local
# docker-compose. Three small, near-identical Dockerfiles — one per
# service, each hardcoding which cmd/ package it builds — sidesteps that
# instead of depending on a Railway feature this repo can't verify live.
#
# shards.yaml/tiers.yaml are baked into the image rather than bind-mounted
# (docker-compose's approach): Railway has no equivalent of a host bind
# mount for arbitrary config files, so SHARDS_CONFIG/TIERS_CONFIG point at
# a path this Dockerfile fills in at build time. Edit-and-redeploy is the
# update path here, matching migrations/deploy/tiers.yaml's own "edit
# freely, no code changes required" framing.
FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -o /out/service ./cmd/api

FROM alpine:3.20
RUN apk add --no-cache ca-certificates
COPY --from=build /out/service /usr/local/bin/service
COPY deploy/shards.yaml /etc/chat/shards.yaml
COPY deploy/tiers.yaml /etc/chat/tiers.yaml
ENTRYPOINT ["/usr/local/bin/service"]
