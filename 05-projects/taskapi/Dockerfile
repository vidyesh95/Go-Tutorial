# syntax=docker/dockerfile:1
#
# Build from the REPOSITORY ROOT (the Go module root), because the module's go.mod lives there:
#
#   docker build -f 05-projects/taskapi/Dockerfile -t taskapi .
#   docker run --rm -p 8080:8080 -e TASKAPI_JWT_SECRET="$(openssl rand -base64 48)" -v taskapi-data:/data taskapi
#
# Two stages: a full Go toolchain image compiles the binary; the runtime image contains ONLY that
# binary. The result is small, has no shell or package manager (nothing for an attacker to use),
# and runs as a non-root user.

FROM golang:1.27 AS build
WORKDIR /src

# Cache mounts keep the module download cache and the compiler cache between builds without
# adding them to the image. (No separate "go mod download" step: go.mod also lists dev tools such
# as sqlc whose large dependency trees the build does not need.)
COPY . .
# CGO_ENABLED=0: modernc.org/sqlite is pure Go, so the binary is fully static and runs on the
#   minimal "static" base image with no libc.
# -trimpath: remove local file system paths from the binary (reproducible builds).
# -ldflags "-s -w": strip the symbol table and DWARF debug info to shrink the binary.
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/taskapi ./05-projects/taskapi/cmd/api
# The runtime image has no shell, so create the data directory here and copy it across.
RUN mkdir /data

FROM gcr.io/distroless/static-debian12:nonroot
# 65532 is the "nonroot" user baked into distroless; it must own the directory SQLite writes to.
COPY --from=build --chown=65532:65532 /data /data
COPY --from=build /out/taskapi /taskapi

ENV TASKAPI_ADDR=:8080 \
    TASKAPI_DB_PATH=/data/taskapi.db \
    TASKAPI_LOG_FORMAT=json
# TASKAPI_JWT_SECRET is deliberately NOT set here: it is required at run time and must never be
# baked into an image layer.

VOLUME /data
EXPOSE 8080
USER nonroot:nonroot
# Exec form so the Go process is PID 1 and receives SIGTERM from "docker stop" directly,
# which triggers the graceful shutdown.
ENTRYPOINT ["/taskapi"]
