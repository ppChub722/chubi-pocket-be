# --- Build stage ---
FROM golang:1.25-alpine AS builder

WORKDIR /app

# Cache deps separately from source
COPY go.mod go.sum ./
RUN go mod download

COPY . .
# Static binary, stripped — keeps the runtime image small and portable.
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /out/server ./cmd/api

# --- Runtime stage ---
FROM alpine:3.20

WORKDIR /app

# CA certs for any outbound HTTPS (Phase 2+ email/OAuth/etc.)
RUN apk add --no-cache ca-certificates && \
    adduser -D -H -u 10001 app

COPY --from=builder /out/server .
RUN chown app:app /app/server

USER app
EXPOSE 8080
CMD ["./server"]
