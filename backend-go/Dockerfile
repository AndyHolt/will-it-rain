# Cloud Run image for the Go backend.
#
# Build context is backend-go/, not repo root.
#
# docker build -f backend-go/Dockerfile -t backend-go:latest backend-go

# Pinned to the builder's own architecture so the toolchain cross-compiles
# rather than the whole stage running under QEMU. Building `--platform
# linux/amd64` from an arm64 Mac is the normal case here, and emulating a Go
# build is minutes where cross-compiling is seconds.
FROM --platform=$BUILDPLATFORM golang:1.26 AS build

WORKDIR /src

# Module files on their own layer, before the source: a source-only rebuild
# then reuses the downloaded dependencies instead of refetching them.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# CGO off is what makes the binary static, and static is what lets the runtime
# stage carry no libc at all. Nothing in the tree needs cgo — leaves is pure
# Go, which was part of why it was chosen over a compiled-.so approach.
#
# GOARCH is pinned rather than taken from the target platform, so an image
# built without `--platform linux/amd64` still holds a binary Cloud Run can
# run. The buildx flag is what stops the *manifest* being tagged arm64.
#
# -trimpath drops local build paths; -s -w drop the symbol table and DWARF,
# which panics do not need (their tracebacks come from the pclntab).
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server

# static-debian12 carries CA certificates — needed, since every outbound call
# is TLS — and little else: no shell, no package manager, nothing to patch.
# :nonroot runs as uid 65532; the service writes nothing to disk.
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/server /server

# Documentation only, and deliberately no `ENV PORT`: cmd/server defaults the
# port when Cloud Run injects none, and defaulting it twice is how the two
# come to disagree.
EXPOSE 8080

ENTRYPOINT ["/server"]
