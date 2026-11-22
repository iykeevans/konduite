# Build from the repository root: go.work ties backend to packages/contracts
# (the contracts module's dependency sums live in go.work.sum).
#
#   docker build -f backend/Dockerfile --target api    -t konduite-api .
#   docker build -f backend/Dockerfile --target worker -t konduite-worker .
#   docker build -f backend/Dockerfile --target natsbootstrap -t konduite-natsbootstrap .
#   docker build -f backend/Dockerfile --target migrate -t konduite-migrate .
#
# No configuration is baked in: every image reads its environment at start.
FROM golang:1.27-alpine AS build
WORKDIR /src
ENV CGO_ENABLED=0 GOFLAGS=-trimpath

COPY go.work go.work.sum ./
COPY runtime/go.mod runtime/go.sum runtime/
COPY packages/contracts packages/contracts
COPY backend backend
COPY tools/go.mod tools/go.sum tools/
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    cd backend && \
    go build -ldflags="-s -w" -o /out/api ./cmd/api && \
    go build -ldflags="-s -w" -o /out/worker ./cmd/worker && \
    go build -ldflags="-s -w" -o /out/natsbootstrap ./cmd/natsbootstrap && \
    go build -ldflags="-s -w" -o /out/healthcheck ./cmd/healthcheck && \
    cd ../tools && \
    GOWORK=off go build -ldflags="-s -w" -o /out/goose github.com/pressly/goose/v3/cmd/goose

FROM gcr.io/distroless/static:nonroot AS api
COPY --from=build /out/api /out/healthcheck /usr/local/bin/
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/api"]

FROM gcr.io/distroless/static:nonroot AS worker
COPY --from=build /out/worker /usr/local/bin/
ENTRYPOINT ["/usr/local/bin/worker"]

# One-shot job: creates or updates the JetStream streams. Reads NATS_URL.
FROM gcr.io/distroless/static:nonroot AS natsbootstrap
COPY --from=build /out/natsbootstrap /usr/local/bin/
ENTRYPOINT ["/usr/local/bin/natsbootstrap"]

# One-shot job: goose reads GOOSE_DBSTRING (set it from DATABASE_URL at run
# time); the default command applies every pending migration.
FROM gcr.io/distroless/static:nonroot AS migrate
COPY --from=build /out/goose /usr/local/bin/
COPY backend/migrations /migrations
ENV GOOSE_DRIVER=postgres GOOSE_MIGRATION_DIR=/migrations
ENTRYPOINT ["/usr/local/bin/goose"]
CMD ["up"]
