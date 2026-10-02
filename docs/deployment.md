# caddyscope on Docker Swarm

Two ways to run the dashboard in a swarm. Both use the same image.

## Password hash

```sh
caddy hash-password
```

Docker Compose and `docker stack deploy` expand `$VAR` in stack files.
Write every `$` in the hash as `$$`. The examples below avoid this by
reading the hash from an env var on the caddy service, using the Caddyfile
placeholder `{$CADDYSCOPE_PASSWORD_HASH}`. The env var itself still needs
`$$` when it is set inline in the stack file.

## Option A: static Caddyfile

[Caddyfile](./swarm/Caddyfile) is a plain config. Mount it into the service and point
Caddy at it, see [stack-static.yml](./swarm/stack-static.yml).

```sh
docker stack deploy -c docs/swarm/stack-static.yml caddy
```

## Option B: caddy-docker-proxy labels

caddy-docker-proxy writes the Caddyfile from service labels and reloads on
every change. The dashboard is just another site block. Put its labels on
the caddy service itself, see [stack-labels.yml](./swarm/stack-labels.yml).

```sh
docker stack deploy -c docs/swarm/stack-labels.yml caddy
```

Label rules that matter here:

- labels without a site address, like `caddy.metrics.per_host`, become global options
- `caddy_0`, `caddy_1`, ... keep separate site blocks apart
- an empty value renders a bare keyword, so `caddy.metrics.per_host: ""` becomes `per_host`
- `{$VAR}` is expanded when the Caddyfile is parsed, from the caddy process env, not from the labelled service
- swarm mode reads `deploy.labels`, not top-level `labels`

Generated Caddyfile from `swarm/stack-labels.yml`:

```caddyfile
{
    metrics {
        per_host
    }
}
scope.example.com {
    caddyscope /dashboard {
        username admin
        password {$CADDYSCOPE_PASSWORD_HASH}
        refresh 5s
    }
}
app.example.com {
    reverse_proxy app:3000
}
```

## Notes

- Leave `observe_catchall_hosts` off in production. Every vhost has a site block, so each one gets its own metrics label anyway. Unknown hosts are grouped under `_other`.
- caddyscope handles config reloads cleanly. Open dashboard streams are closed on process exit so the container stops without waiting for the grace period.
