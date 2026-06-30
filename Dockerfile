# Stage 1: Build the Vue frontend
FROM node:24-alpine AS frontend-builder
WORKDIR /app/web
COPY web/package*.json ./
RUN npm install
COPY web/ ./
RUN npm run build

# Stage 2: Build the Go backend.
# Pin the builder to the native build host ($BUILDPLATFORM) and cross-compile to
# the requested target arch. CGO is disabled, so Go cross-compiles trivially and
# we avoid slow QEMU emulation when producing multi-arch images.
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS backend-builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
# Copy Go package sources explicitly to optimize build caching
COPY cmd/ ./cmd/
COPY internal/ ./internal/
COPY web/embed.go ./web/embed.go
COPY web/src/test/fixtures/ ./web/src/test/fixtures/
# Copy built frontend assets from stage 1 into Go web/dist
COPY --from=frontend-builder /app/web/dist/ ./web/dist/
# Run unit tests using the same CGO flag used for compiling to guarantee build
# health. Tests are architecture-independent and this layer is kept BEFORE the
# ARG TARGETARCH below, so buildkit runs it once natively and reuses the cached
# layer across every target platform.
RUN CGO_ENABLED=0 go test -v ./...
# Compile statically linked Go binary (CGO_ENABLED=0 to avoid glibc dependencies)
# for the platform buildx is currently targeting (TARGETOS/TARGETARCH are
# injected automatically per --platform entry).
ARG TARGETOS TARGETARCH
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -ldflags="-w -s" -o /app/bin/folio-idx cmd/folio-idx/main.go
# Create a dedicated directory for SQLite database files and set owner to nonroot (65532)
RUN mkdir -p /data && chown -R 65532:65532 /data

# Stage 3: Run target
FROM gcr.io/distroless/static-debian12
WORKDIR /app
# Copy the compiled binary (remains owned by root, read-only to nonroot for security)
COPY --from=backend-builder /app/bin/folio-idx .
# Copy the data directory with nonroot ownership (UID/GID 65532)
COPY --from=backend-builder --chown=65532:65532 /data /data
# Use the built-in nonroot user
USER 65532:65532
EXPOSE 8080
ENV APP_ENV=production
ENV PORT=8080
# DATA_DIR must point at the mounted volume, not the default ./data (which would
# resolve to /app/data — a root-owned, read-only dir the nonroot user can't write).
ENV DATA_DIR=/data
# Optional defense-in-depth: set LIBRARY_ROOT to your books mount (e.g. /library)
# to confine every library path to that subtree, so a compromised admin session
# cannot point a library at an arbitrary host path. Left unset here to preserve
# the default "any path" behavior; opt in at deploy time.
CMD ["/app/folio-idx"]
