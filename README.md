# Flowdraw

Flowdraw is a React/Vite diagram editor built on Excalidraw, with animated flows, junctions, data packets, and animated exports.

## Local development

Requires Node.js 22 and pnpm 10.15.1.

```sh
corepack enable
pnpm install --frozen-lockfile
pnpm dev
```

Run `pnpm lint`, `pnpm test`, and `pnpm build` before submitting changes.

## Deployment

GitHub Actions builds production images and pushes them to GHCR; the VPS only pulls and runs images. See [the deployment guide](docs/deployment.md) for Hetzner, Docker Compose, Caddy, GHCR, CI/CD, and rollback instructions.
