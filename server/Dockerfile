# Build stage — compile the Go binary
FROM golang:1.24-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /open-cache ./cmd/open-cache/

# Runtime stage — minimal image with just the binary
FROM alpine:3.21

RUN apk add --no-cache ca-certificates
COPY --from=build /open-cache /usr/local/bin/open-cache

EXPOSE 8080
ENTRYPOINT ["open-cache"]
