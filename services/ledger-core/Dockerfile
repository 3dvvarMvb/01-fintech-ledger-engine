# Stage 1: Build binary
FROM golang:1.23-alpine AS builder

WORKDIR /app

RUN apk add --no-cache git

COPY go.mod ./
COPY main.go ./
COPY dashboard.html ./

# Download dependencies and generate clean go.sum
RUN go mod tidy
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s" -o /app/ledger-service .

# Stage 2: Minimal Production Image
FROM alpine:3.20

RUN apk add --no-cache wget ca-certificates

RUN adduser -D -u 1000 appuser
USER appuser

WORKDIR /app
COPY --from=builder /app/ledger-service /app/ledger-service

EXPOSE 8080

HEALTHCHECK --interval=5s --timeout=3s --retries=3 \
  CMD wget --quiet --tries=1 --spider http://localhost:8080/health || exit 1

ENTRYPOINT ["/app/ledger-service"]
