# Pin a specific toolbox version. Update intentionally.
ARG TOOLBOX_VERSION=1.1.0

# Stage 1: pull the toolbox binary out of the official image.
FROM us-central1-docker.pkg.dev/database-toolbox/toolbox/toolbox:${TOOLBOX_VERSION} AS toolbox

# Stage 2: build the tokeninfo sidecar (Auth v2.5, issue #7).
# Stdlib-only Go binary — no external dependencies.
FROM golang:1.22-alpine AS sidecar-build
WORKDIR /src
COPY sidecar/go.mod ./
RUN go mod download
COPY sidecar/ ./
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /tokeninfo-sidecar .

# Stage 3: Caddy as the runtime, with toolbox + sidecar bundled in.
# The `caddy:2` image is Alpine-based (musl), but the toolbox binary is
# dynamically linked against glibc. Without a shim, exec'ing toolbox
# inside this container fails with a misleading "no such file or
# directory" from the dynamic linker. `gcompat` + `libc6-compat`
# provide a musl→glibc translation layer that's enough for toolbox.
FROM caddy:2

RUN apk add --no-cache gcompat libc6-compat bash

COPY --from=toolbox /toolbox /usr/local/bin/toolbox
COPY --from=sidecar-build /tokeninfo-sidecar /usr/local/bin/tokeninfo-sidecar
COPY tools.yaml      /app/tools.yaml
COPY Caddyfile       /etc/caddy/Caddyfile
COPY entrypoint.sh   /entrypoint.sh
RUN chmod +x /entrypoint.sh

EXPOSE 8080
ENTRYPOINT ["/entrypoint.sh"]
