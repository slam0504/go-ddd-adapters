# Build stage — compiles all three example binaries from the local module.
# The build context is `..` (parent dir) so we can pick up the adapter root
# referenced via the example's `replace` directive.
FROM golang:1.24-alpine AS build

WORKDIR /src

# Copy adapter root sources first so the cache survives example-only edits.
COPY go.mod go.sum ./
COPY eventbus eventbus
COPY logger logger
COPY observability observability

# Now copy the example sub-module and resolve its deps.
COPY examples/orders examples/orders

WORKDIR /src/examples/orders
RUN go mod download

ARG TARGETOS=linux
ARG TARGETARCH=amd64
ENV CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH}

RUN go build -trimpath -ldflags='-s -w' -o /out/orders-api ./cmd/api && \
    go build -trimpath -ldflags='-s -w' -o /out/orders-worker ./cmd/worker && \
    go build -trimpath -ldflags='-s -w' -o /out/orders-reader ./cmd/reader

# Runtime stage — distroless static, holds all three binaries. Each
# service in docker-compose picks one via the `command` override.
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/orders-api /orders-api
COPY --from=build /out/orders-worker /orders-worker
COPY --from=build /out/orders-reader /orders-reader

USER nonroot:nonroot
