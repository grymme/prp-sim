# Dockerfile — multi-stage build for PRP GNS3 container
FROM golang:1.22-alpine AS builder
WORKDIR /build
COPY go.mod .
RUN go mod download
COPY . .
RUN go build -o /prpd ./cmd/prpd

FROM alpine:3.19
RUN apk add --no-cache iproute2 busybox-extras
COPY --from=builder /prpd /usr/local/bin/prpd
COPY config.yaml /etc/prp/config.yaml
COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh
ENTRYPOINT ["/entrypoint.sh"]
