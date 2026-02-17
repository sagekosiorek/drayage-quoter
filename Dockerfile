# Build stage
FROM golang:1.26 AS build

WORKDIR /app
COPY go.mod go.sum* ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /drayage-quoter ./cmd/server

# Release stage
FROM gcr.io/distroless/base-debian12 AS release

WORKDIR /
COPY --from=build /drayage-quoter /drayage-quoter

EXPOSE 8080

USER nonroot:nonroot

ENTRYPOINT ["/drayage-quoter"]
