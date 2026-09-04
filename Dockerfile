# Stage 1: Build
FROM golang:1.22-alpine AS builder
WORKDIR /app
RUN apk add --no-cache git ca-certificates
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /app/distrimax ./cmd/distrimax

# Stage 2: Minimal rootless runtime
FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -S distrimax && adduser -S distrimax -G distrimax \
    && mkdir -p /var/lib/distrimax/artifacts /var/lib/distrimax/staging /var/lib/distrimax/backups \
    && chown -R distrimax:distrimax /var/lib/distrimax

USER distrimax
WORKDIR /var/lib/distrimax

COPY --from=builder /app/distrimax /usr/local/bin/distrimax

EXPOSE 8080
VOLUME ["/var/lib/distrimax"]
ENTRYPOINT ["/usr/local/bin/distrimax"]
