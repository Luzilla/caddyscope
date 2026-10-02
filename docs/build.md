# Build

This `Dockerfile` builds Caddy with caddyscope and [caddy-docker-proxy](https://github.com/lucaslorentz/caddy-docker-proxy).

Drop the proxy line if you only want the static setup.

```dockerfile
ARG CADDY_VERSION=2.11.2

FROM caddy:${CADDY_VERSION}-builder AS builder

RUN xcaddy build \
    --with github.com/lucaslorentz/caddy-docker-proxy/v2 \
    --with github.com/luzilla/caddyscope

FROM caddy:${CADDY_VERSION}

COPY --from=builder /usr/bin/caddy /usr/bin/caddy

# Only for the label setup (stack-labels.yml). The static setup
# overrides this with `caddy run --config /etc/caddy/Caddyfile`.
CMD ["caddy", "docker-proxy"]
```

And build with:

```sh
docker build -t your-registry/caddy:latest docs/swarm
```
