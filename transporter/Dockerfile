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
RUN adduser -D -H -u 10001 transporter
COPY --from=builder /out/transporter /usr/local/bin/transporter
COPY transporter/config.example.json /app/config.json

WORKDIR /app
USER transporter
EXPOSE 9000
ENTRYPOINT ["transporter"]
