# Build stage
FROM golang:1.24-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o server ./cmd/server
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o portal-cron ./cmd/cron

# Runtime stage
FROM alpine:latest
RUN apk --no-cache add ca-certificates tzdata
WORKDIR /app
COPY --from=builder /app/server .
COPY --from=builder /app/portal-cron .
COPY --from=builder /app/web/templates ./web/templates
COPY --from=builder /app/web/static ./web/static
COPY --from=builder /app/migrations ./migrations
# Port is configured via PORT env variable
CMD ["./server"]
