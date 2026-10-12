# Build stage
FROM golang:1.22-alpine AS builder
RUN apk add --no-cache git
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /dynago ./cmd/dynago

# Runtime stage
FROM alpine:3.20
RUN apk add --no-cache ca-certificates
COPY --from=builder /dynago /usr/local/bin/dynago
COPY plugins/ /app/plugins/
WORKDIR /app
EXPOSE 8080
ENTRYPOINT ["dynago"]
