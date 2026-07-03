# Build stage
FROM golang:1.24-alpine AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o ai-gateway .

# Runtime stage
FROM alpine:3.19

RUN apk --no-cache add ca-certificates tzdata

WORKDIR /app
COPY --from=builder /app/ai-gateway .
COPY --from=builder /app/gateway.yaml .
COPY --from=builder /app/demo.html .
COPY --from=builder /app/dashboard.html .

EXPOSE 8080
EXPOSE 9090

ENV PORT=8080
ENV REDIS_URL=redis://localhost:6379

HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
  CMD wget --no-verbose --tries=1 --spider http://localhost:8080/health || exit 1

ENTRYPOINT ["./ai-gateway"]