# Pilot deployment runbook

The pilot runs the API, worker and web app with docker compose on one VM. Postgres and Auth are hosted Supabase; the event bus is a hosted NATS JetStream. Nothing stateful runs on the VM, so it can be rebuilt at any time.

Status: the images and compose file are built and checked in CI. The live deployment (steps below on a real host with real credentials) has not been done yet; PLAT-013's "deployed to a pilot environment" acceptance item stays open until it is.

## What ships

| Image | Dockerfile target | Purpose |
| --- | --- | --- |
| `api` | `backend/Dockerfile` `api` | HTTP API on :8080, `/healthz` and `/readyz` |
| `worker` | `backend/Dockerfile` `worker` | outbox relay and consumers |
| `migrate` | `backend/Dockerfile` `migrate` | one-shot `goose up` against the hosted database |
| `nats-bootstrap` | `backend/Dockerfile` `natsbootstrap` | one-shot JetStream stream create/update |
| `web` | `apps/web/Dockerfile` | Next.js dashboard on :3000 |

Go images are distroless and run as a non-root user. No configuration is baked into any image: everything comes from the environment at start.

## Prerequisites

- A Linux VM with Docker Engine and the compose plugin, outbound access to Supabase and NATS, and a TLS-terminating reverse proxy (Caddy, nginx or a cloud load balancer) in front of ports 8080 and 3000. The proxy must pass WebSocket upgrades so the runtime connector's connection can go through the API once it lands.
- A hosted Supabase project: database password, project URL, publishable key.
- A hosted NATS with JetStream enabled and credentials.

## Configuration and secrets

1. `cp infrastructure/deployment/pilot.env.example infrastructure/deployment/.env` on the VM. `.env` is gitignored; never commit it or paste its values into issues, logs or CI.
2. Fill in every value. Required by the backend in production: `DATABASE_URL`, `SUPABASE_URL`, `NATS_URL`, `RUNTIME_TOKEN_SIGNING_KEY` (`openssl rand -base64 32`). `PLATFORM_ADMIN_USER_IDS` is optional; empty denies everyone on `/api/v1/admin`.
3. `NEXT_PUBLIC_*` values are public by design: they are inlined into the browser bundle at build time. Only the publishable key goes there, never the service-role key.
4. `chmod 600` the file and restrict who can read it on the host.

Compose refuses to start if a required variable is unset, and the API and worker exit non-zero with a list of every invalid variable.

## Deploy

Run from the repository root. Define a shell alias once:

```sh
dc() { docker compose -f infrastructure/deployment/docker-compose.pilot.yml --env-file infrastructure/deployment/.env "$@"; }
```

Order matters:

```sh
dc --profile ops build
dc --profile ops run --rm migrate          # goose up
dc --profile ops run --rm nats-bootstrap   # streams
dc up -d --build api worker web
dc ps                                      # api should report healthy
```

## Health checks

- `curl -fsS http://127.0.0.1:8080/healthz` is liveness; `/readyz` checks the database and NATS.
- `dc ps` shows the compose healthchecks (`api` via the bundled `healthcheck` binary, `web` via wget).
- `dc logs -f api worker` for startup errors. Logs never include secrets; if you find one, treat it as a bug and rotate that secret.
- Smoke test: sign in on the web app, create a workspace, connect a runtime.

## Updating

```sh
git pull
dc --profile ops build
dc --profile ops run --rm migrate
dc --profile ops run --rm nats-bootstrap
dc up -d --build api worker web
```

Migrations run before the new API starts, so write them to be compatible with the previous release for one deploy.

## Rollback

1. `git checkout <previous tag or commit>` and rebuild: `dc up -d --build api worker web`.
2. Migrations are not rolled back automatically. Only run `dc --profile ops run --rm migrate down` if the migration being undone has a tested `Down` and no data depends on it. When unsure, roll forward with a fix.

## Key rotation

- `RUNTIME_TOKEN_SIGNING_KEY`: generate a new seed, update `.env`, `dc up -d api worker`. Every issued runtime token stops verifying, so every runtime must re-register.
- Database password: reset it in Supabase, update `DATABASE_URL`, `dc up -d api worker`, rerun `migrate` next deploy.
- Publishable key: update `NEXT_PUBLIC_SUPABASE_PUBLISHABLE_KEY` and rebuild `web` (`dc up -d --build web`).

## Troubleshooting

- `api` exits at start with `invalid configuration`: the message lists each missing or malformed variable.
- `api` unhealthy but running: `curl /readyz` shows which dependency (database or NATS) fails. Check egress and that the Supabase connection is the direct or session pooler (port 5432).
- `migrate` hangs or errors with prepared statement messages: you are on the transaction pooler; switch to the session pooler or direct connection.
- Worker idle with messages piling up: rerun `nats-bootstrap`, then check `dc logs worker`.
- `web` redirect loops to `/login`: `NEXT_PUBLIC_SUPABASE_*` were wrong at build time; fix `.env` and rebuild `web`.

## Open

- The first live deployment, with real hosted credentials and a host, is not done. When it is, record the host, URL and date here and close PLAT-013.
