# Build context is the repository root (the go.work workspace root), e.g.:
#   podman build -f transporter/Dockerfile -t adb-remote-transporter .

FROM golang:1.24-alpine AS builder
WORKDIR /src

COPY go.work go.work.sum ./
COPY shared/ shared/
COPY client/ client/
COPY transporter/ transporter/

RUN CGO_ENABLED=0 go build -o /out/transporter ./transporter

FROM alpine:3.20
# /app (the config) stays root-owned and read-only for the service user;
# /data is the one place it writes to: the TLS identity generated on first
# start. Mount a volume there to keep that identity across containers.
RUN adduser -D -H -u 10001 transporter \
 && mkdir -p /data \
 && chown transporter:transporter /data \
 && chmod 0700 /data
COPY --from=builder /out/transporter /usr/local/bin/transporter
COPY transporter/config.docker.json /app/config.json

WORKDIR /app
USER transporter
VOLUME ["/data"]
EXPOSE 9000
ENTRYPOINT ["transporter"]
