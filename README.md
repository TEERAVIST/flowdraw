# Flowdraw

Flowdraw is a React/Vite diagram editor built on Excalidraw, with animated flows, junctions, data packets, animated exports, and room-based live collaboration.

## Local development

Requires Node.js 22 and pnpm 10.15.1.

```sh
corepack enable
pnpm install --frozen-lockfile
pnpm dev
```

The browser can run locally without the collaboration services. To test live rooms, PostgreSQL, and MinIO together, use the production Compose stack described in the deployment guide.

Run `pnpm lint`, `pnpm test`, and `pnpm build` before submitting changes.

## Deployment

GitHub Actions builds frontend and API images and pushes them to GHCR; the VPS only pulls and runs images. See [the deployment guide](docs/deployment.md) for Hetzner, Docker Compose, Caddy, PostgreSQL, MinIO, GHCR, CI/CD, backups, and rollback instructions.

## Central authentication

The optional Auth V1 service uses Go, Fosite, Resend and a separate PostgreSQL database. Flowdraw's backend owns its OIDC exchange and product sessions; React receives no OAuth tokens. See [architecture and deployment](docs/auth-architecture.md) and [validation and rollout blockers](docs/auth-validation.md). Provision auth explicitly before enabling the Flowdraw auth environment variables.
