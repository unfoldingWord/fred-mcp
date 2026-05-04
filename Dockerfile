# Pin a specific toolbox version. Update intentionally.
ARG TOOLBOX_VERSION=1.1.0

# Stage 1: pull the toolbox binary out of the official image.
FROM us-central1-docker.pkg.dev/database-toolbox/toolbox/toolbox:${TOOLBOX_VERSION} AS toolbox

# Stage 2: Caddy as the runtime, with toolbox bundled in.
# The `caddy:2` image is Alpine-based (musl), but the toolbox binary is
# dynamically linked against glibc. Without a shim, exec'ing toolbox
# inside this container fails with a misleading "no such file or
# directory" from the dynamic linker. `gcompat` + `libc6-compat`
# provide a musl→glibc translation layer that's enough for toolbox.
FROM caddy:2

RUN apk add --no-cache gcompat libc6-compat

COPY --from=toolbox /toolbox /usr/local/bin/toolbox
COPY tools.yaml      /app/tools.yaml
COPY Caddyfile       /etc/caddy/Caddyfile
COPY entrypoint.sh   /entrypoint.sh
RUN chmod +x /entrypoint.sh

EXPOSE 8080
ENTRYPOINT ["/entrypoint.sh"]
