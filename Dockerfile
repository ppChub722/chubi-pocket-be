FROM golang:1.25-alpine AS builder

WORKDIR /app

# Install migrate CLI for running migrations
RUN go install -tags 'postgres' github.com/golang-migrate/migrate/v4/cmd/migrate@latest

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN go build -o server cmd/api/main.go

# ---

FROM alpine:3.20

WORKDIR /app

COPY --from=builder /app/server .
COPY --from=builder /app/migrations ./migrations
COPY --from=builder /go/bin/migrate /usr/local/bin/migrate

EXPOSE 8080

CMD ["./server"]
