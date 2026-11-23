# syntax=docker/dockerfile:1
#
# ONE Dockerfile builds ANY of the three services; pick with --build-arg SERVICE=...
# Build from the REPOSITORY ROOT (the Go module root; go.mod lives there):
#
#   docker build -f 07-microservices/Dockerfile --build-arg SERVICE=users-svc  -t users-svc  .
#   docker build -f 07-microservices/Dockerfile --build-arg SERVICE=orders-svc -t orders-svc .
#   docker build -f 07-microservices/Dockerfile --build-arg SERVICE=gateway    -t gateway    .
#
# (docker-compose.yml does exactly this for you.) Two stages: a full Go toolchain compiles the
# binary; the runtime image contains ONLY that binary, no shell or package manager, and runs as
# a non-root user.

FROM golang:1.27 AS build
# Which directory under 07-microservices to build: users-svc | orders-svc | gateway
ARG SERVICE
WORKDIR /src
COPY . .
# CGO_ENABLED=0: modernc.org/sqlite is pure Go, so the binary is fully static and runs on the
#   "static" distroless image with no libc.
# -trimpath removes local paths from the binary; -s -w strip symbol and debug info (smaller).
RUN test -n "$SERVICE" || (echo "pass --build-arg SERVICE=users-svc|orders-svc|gateway" >&2; exit 1)
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/app ./07-microservices/${SERVICE}
# The runtime image has no shell, so create the data directory here and copy it across.
RUN mkdir /data

FROM gcr.io/distroless/static-debian12:nonroot
# 65532 is the "nonroot" user baked into distroless; it must own the directory SQLite writes to.
COPY --from=build --chown=65532:65532 /data /data
COPY --from=build /out/app /app
VOLUME /data
USER nonroot:nonroot
# Exec form: the Go process is PID 1 and receives SIGTERM from `docker stop` directly, which
# triggers the graceful shutdown. ADDR, DB_PATH, USERS_ADDR, ORDERS_URL are set per service by
# docker-compose.yml. The binary also supports `/app -healthcheck` (see HEALTHCHECK in compose).
ENTRYPOINT ["/app"]
