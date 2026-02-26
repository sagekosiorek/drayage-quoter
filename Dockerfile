# Build stage
FROM golang:1.26 AS build

WORKDIR /app
COPY go.mod go.sum* ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /drayage-quoter ./cmd/server && \
    CGO_ENABLED=0 GOOS=linux go build -o /seed ./cmd/seed

# Litestream stage — copy binary from official image.
FROM litestream/litestream:0.3.13 AS litestream

# Release stage
FROM debian:bookworm-slim AS release

RUN apt-get update && apt-get install -y ca-certificates && rm -rf /var/lib/apt/lists/*

WORKDIR /
COPY --from=build /drayage-quoter /drayage-quoter
COPY --from=build /seed /seed
COPY --from=litestream /usr/local/bin/litestream /usr/local/bin/litestream
COPY litestream.prod.yml /etc/litestream.yml
COPY entrypoint.sh /entrypoint.sh

RUN chmod +x /entrypoint.sh

EXPOSE 8080

ENTRYPOINT ["/entrypoint.sh"]
