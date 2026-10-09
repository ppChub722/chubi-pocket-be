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
# tesseract-ocr — slip OCR (0.3.1), run as a CLI by internal/modules/imports/ocr.
RUN apk add --no-cache ca-certificates tesseract-ocr && \
    adduser -D -H -u 10001 app

# Thai + English models from tessdata_best (LSTM, most accurate; pinned to
# the 4.1.0 tag). Overwrites the package's default eng if it brought one.
ADD --chmod=644 https://github.com/tesseract-ocr/tessdata_best/raw/4.1.0/tha.traineddata \
    https://github.com/tesseract-ocr/tessdata_best/raw/4.1.0/eng.traineddata \
    /usr/share/tessdata/

COPY --from=builder /out/server .
RUN chown app:app /app/server

USER app
EXPOSE 8080
CMD ["./server"]
