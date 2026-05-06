# syntax=docker/dockerfile:1.7

FROM golang:1.23-alpine AS build
WORKDIR /src
RUN apk add --no-cache git ca-certificates
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=0.1.0-phase1
RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath \
    -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/platform ./cmd/platform

FROM gcr.io/distroless/static-debian12:nonroot AS runtime
WORKDIR /app
COPY --from=build /out/platform /app/platform
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/app/platform"]
