# Flowdraw production deployment

## Architecture

GitHub Actions runs pnpm checks, builds the frontend and API images, pushes immutable `sha-<commit>` tags to GHCR, and deploys those exact images over SSH. The VPS only pulls and runs images. A separately managed Caddy gateway owns ports 80/443 behind Cloudflare. Cloudflare is the public edge, Caddy is the platform ingress, Docker runs the workloads, and PostgreSQL is the source of truth. The API provides WebSocket room synchronization, while MinIO stores shared binary objects.

```text
/opt/apps/
├── gateway/
│   ├── compose.yml
│   └── Caddyfile
├── flowdraw/
│   ├── compose.yml
│   └── .env
├── auth/       # future
└── app2/       # future
```

No application, database, or object store publishes a host port. Only Caddy is internet-facing. PostgreSQL and MinIO communicate with the API on an internal Docker network.

Compose `expose` entries document container ports for peer services; unlike `ports`, they do not publish those ports on the VPS host. The external `proxy` network is shared with the gateway. The project-scoped `backend` network is internal and belongs only to Flowdraw's API, PostgreSQL, and MinIO services.

## A. One-time VPS setup

Connect to Ubuntu as the initial administrator:

```sh
ssh root@YOUR_VPS_IP
apt-get update
apt-get upgrade -y
```

Configure the Hetzner firewall for SSH, TCP 80, TCP 443, and UDP 443. Restrict SSH by source IP when practical. After HTTPS works, optionally restrict web ingress to Cloudflare's published IP ranges; keep those ranges updated and preserve a recovery path through the Hetzner console.

## B–C. Install Docker Engine and Compose

```sh
apt-get update
apt-get install -y ca-certificates curl
install -m 0755 -d /etc/apt/keyrings
curl -fsSL https://download.docker.com/linux/ubuntu/gpg -o /etc/apt/keyrings/docker.asc
chmod a+r /etc/apt/keyrings/docker.asc
. /etc/os-release
echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/ubuntu $VERSION_CODENAME stable" > /etc/apt/sources.list.d/docker.list
apt-get update
apt-get install -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
docker version
docker compose version
```

## D. Create the deploy user

```sh
adduser --disabled-password --gecos "" deploy
usermod -aG docker deploy
install -d -m 700 -o deploy -g deploy /home/deploy/.ssh
touch /home/deploy/.ssh/authorized_keys
chown deploy:deploy /home/deploy/.ssh/authorized_keys
chmod 600 /home/deploy/.ssh/authorized_keys
```

Append the deployment public key to `authorized_keys`. Store its private key only as GitHub secret `VPS_SSH_KEY`. Docker-group membership is root-equivalent, so protect this account and key.

## E–F. Directories and shared network

```sh
mkdir -p /opt/apps/gateway /opt/apps/flowdraw
chown -R deploy:deploy /opt/apps
docker network create proxy
```

Copy infrastructure files once from the development machine (not application source):

```sh
scp deploy/compose.yml deploy@YOUR_VPS_IP:/opt/apps/flowdraw/compose.yml
scp deploy/gateway.compose.yml deploy@YOUR_VPS_IP:/opt/apps/gateway/compose.yml
scp deploy/Caddyfile.example deploy@YOUR_VPS_IP:/opt/apps/gateway/Caddyfile
scp deploy/gateway.env.example deploy@YOUR_VPS_IP:/opt/apps/gateway/.env
scp deploy/minio-flowdraw-policy.json deploy/provision-minio.sh deploy@YOUR_VPS_IP:/opt/apps/flowdraw/
```

Edit `/opt/apps/gateway/.env`:

```dotenv
FLOWDRAW_HOSTNAME=flowdraw.k1n3ticnerdcore.tech
```

Create `/opt/apps/flowdraw/.env` on the VPS:

```dotenv
GHCR_OWNER=your-github-username-in-lowercase
IMAGE_TAG=sha-full-git-commit
POSTGRES_PASSWORD=long-independent-random-value
MINIO_ROOT_USER=random-access-key
MINIO_ROOT_PASSWORD=long-independent-random-value
FLOWDRAW_S3_ACCESS_KEY=dedicated-flowdraw-access-key
FLOWDRAW_S3_SECRET_KEY=dedicated-flowdraw-secret-key
```

Generate independent values with `openssl rand -base64 32`. The Flowdraw S3 values must not equal either MinIO root value. GitHub Actions rewrites only `GHCR_OWNER` and `IMAGE_TAG`; it preserves the server secrets. Production does not depend on `latest`. Never commit the real `.env`, and set `chmod 600 /opt/apps/flowdraw/.env`.

Start only the data services and provision the bucket-scoped Flowdraw identity once:

```sh
cd /opt/apps/flowdraw
docker compose up -d postgres minio
chmod 700 provision-minio.sh
./provision-minio.sh
```

The provisioning command runs an ephemeral MinIO client container; it does not add another persistent service. Root credentials are used only inside that administrative command and by the MinIO container. `flowdraw-api` receives only `FLOWDRAW_S3_ACCESS_KEY` and `FLOWDRAW_S3_SECRET_KEY`. The attached policy permits bucket inspection/listing and object reads/writes only within `flowdraw`; it does not grant object deletion, console access, or access to other buckets.

## G. Configure Caddy

Edit `/opt/apps/gateway/Caddyfile`:

```caddyfile
{$FLOWDRAW_HOSTNAME} {
    handle /api/* {
        reverse_proxy flowdraw-api:3000
    }
    handle /ws {
        reverse_proxy flowdraw-api:3000
    }
    handle {
        reverse_proxy flowdraw:80
    }
}
```

Then start the shared gateway:

```sh
cd /opt/apps/gateway
docker compose up -d
docker compose ps
```

Caddy is the only service publishing 80/443 and automatically manages the origin certificate after DNS is correct. Caddy automatically proxies WebSocket upgrades on `/ws`; no special WebSocket header configuration is required.

`FLOWDRAW_HOSTNAME` comes from the shared gateway's `.env`. The application image and Flowdraw Compose project therefore do not bake in a production hostname. Add future SaaS hostnames as separate site blocks and environment values in this shared gateway project.

## H. Configure DNS

In the Cloudflare DNS dashboard, create a proxied (orange-cloud) `A` record:

```text
Type: A
Name: flowdraw
Content: YOUR_VPS_IPV4
Proxy status: Proxied
```

Add `AAAA` only when IPv6 is configured on both the VPS and firewall. In Cloudflare SSL/TLS, select **Full (strict)**, enable **Always Use HTTPS**, and leave WebSockets enabled. Do not use Flexible TLS because traffic from Cloudflare to Caddy must remain encrypted and authenticated.

The resulting public URL is `https://flowdraw.k1n3ticnerdcore.tech`. Caddy should still terminate TLS at the origin; Cloudflare does not replace the platform ingress.

## I. Configure GitHub Secrets

Under **Repository Settings → Secrets and variables → Actions**, create:

- `VPS_HOST`: VPS hostname or IP.
- `VPS_USER`: normally `deploy`.
- `VPS_SSH_KEY`: private deployment key including BEGIN/END lines.
- `VPS_PORT`: normally `22`.
- `VPS_KNOWN_HOSTS`: verified SSH host-key line.
- `APP_URL`: `https://flowdraw.k1n3ticnerdcore.tech`.

Generate the known-hosts value from a trusted workstation, then verify its fingerprint against the VPS console:

```sh
ssh-keyscan -p 22 YOUR_VPS_IP
```

Optionally protect the GitHub `production` environment with reviewers.

## J. Authenticate the VPS to private GHCR

Public images require no login. For a private package, create a dedicated GitHub token with minimum `read:packages` permission and access only to the required repository/package. Do not grant write/admin scopes.

Run as `deploy` on the VPS:

```sh
read -s GHCR_TOKEN
printf '%s' "$GHCR_TOKEN" | docker login ghcr.io -u YOUR_GITHUB_USERNAME --password-stdin
unset GHCR_TOKEN
```

Never put this token in Compose, `.env`, the image, or Git. GitHub Actions pushes using `GITHUB_TOKEN` with workflow-scoped `packages: write`.

## K. First deployment

Push to `main`. The workflow installs from the frozen pnpm lockfile, runs lint/test/build, builds and pushes both `flowdraw:sha-COMMIT` and `flowdraw-server:sha-COMMIT`, deploys with `--no-build`, and verifies the frontend and API.

For a manual first start after setting `.env`:

```sh
cd /opt/apps/flowdraw
docker compose pull
docker compose up -d --no-build
docker compose ps
```

On its first start, the API creates the required database tables and the private MinIO bucket. The VPS must not run `git pull`, `pnpm install`, `pnpm build`, `docker build`, or `docker compose build` during deployment.

## Live collaboration and storage

Select **Start live room** in Flowdraw and share the copied invite URL. Its URL fragment contains both the room ID and a 256-bit edit key. Fragments are not sent in HTTP request targets. The browser sends the edit key only as the first WebSocket application message after connecting to `/ws?room=<non-secret-id>`; the server does not log that message. Everyone using the complete link can edit, so treat it like a password and do not post it publicly. The edit key is stored in PostgreSQL only as a SHA-256 hash.

Links made by the earlier query-parameter version are migrated to fragment form when opened. Because the original HTTP request already contained the old query string, rotate the room/link if it may have entered proxy, browser, or analytics logs.

## Health semantics

- `GET /health/live` checks only that the Node HTTP process can respond. Docker uses this endpoint, so a temporary PostgreSQL or MinIO outage does not mark the process dead or trigger a restart loop.
- `GET /health/ready` checks PostgreSQL and the required MinIO bucket. It returns a failure while either dependency is unavailable.
- `/api/health/live` and `/api/health/ready` expose the same checks through the existing Caddy `/api/*` route. `/api/health` remains a readiness alias for compatibility.

Scene elements, flows, junctions, packets, and object references are synchronized through `/ws`. Snapshots use monotonically increasing PostgreSQL revisions; a stale update receives the current server snapshot instead of silently overwriting it. Embedded images and other Excalidraw files are uploaded through the authenticated API and kept in MinIO rather than inside PostgreSQL. Current limits are 5 MB per scene snapshot and 25 MB per object and can be changed with `MAX_SNAPSHOT_BYTES` and `MAX_OBJECT_BYTES` on `flowdraw-api`.

This version uses snapshot-level optimistic concurrency rather than a CRDT. It keeps all clients consistent and prevents stale database writes, but two people changing the same room at precisely the same time may cause one client to reload the winning snapshot. Element-level CRDT merging and user cursors can be added later without changing the storage topology.

## L. Normal deployments

Push to `main`; pull requests only run checks. The deploy job writes `GHCR_OWNER` and `IMAGE_TAG=sha-${GITHUB_SHA}` to `/opt/apps/flowdraw/.env` before pulling, guaranteeing the exact image created by that workflow run.

## M. Status and logs

```sh
cd /opt/apps/flowdraw
docker compose ps
docker compose logs --tail=200 flowdraw
docker compose logs --tail=200 flowdraw-api
docker compose logs --tail=200 postgres
docker compose logs --tail=200 minio
container_id=$(docker compose ps -q flowdraw)
docker inspect --format='{{.State.Health.Status}}' "$container_id"

cd /opt/apps/gateway
docker compose ps
docker compose logs --tail=200 caddy
```

## N. Rollback

Choose a known-good SHA tag from GHCR or a successful workflow:

```sh
cd /opt/apps/flowdraw
sed -i 's/^IMAGE_TAG=.*/IMAGE_TAG=sha-OLD_FULL_COMMIT/' .env
docker compose pull flowdraw
docker compose up -d --no-build flowdraw
docker compose ps
```

No source checkout or rebuild is needed. Repeat with a newer SHA to roll forward.

## Database and object backups

Back up PostgreSQL regularly and copy the resulting archive off the VPS:

```sh
cd /opt/apps/flowdraw
umask 077
docker compose exec -T postgres pg_dump -U flowdraw -d flowdraw -Fc > flowdraw-postgres.dump
```

MinIO data lives in the `minio_data` named volume. Use a provider-level volume snapshot or a dedicated backup container/tool that copies the bucket to remote S3-compatible storage. A database backup without the matching MinIO bucket is incomplete because PostgreSQL stores object metadata while MinIO stores the bytes. Test both restoration paths before relying on them.

## O. Update Caddy

```sh
cd /opt/apps/gateway
docker compose exec caddy caddy validate --config /etc/caddy/Caddyfile
docker compose exec caddy caddy reload --config /etc/caddy/Caddyfile
```

Future apps get separate site blocks pointing to service names on the shared `proxy` network.

## P. Troubleshooting

### Image pull fails

- Ensure `GHCR_OWNER` is lowercase and the SHA tag exists.
- Log in to GHCR as the `deploy` user for private packages.
- Ensure the token has `read:packages` and package access.

### Container is unhealthy

```sh
cd /opt/apps/flowdraw
docker compose ps
docker compose logs --tail=200 flowdraw
docker inspect "$(docker compose ps -q flowdraw)"
```

### Caddy returns 502

Run `docker network inspect proxy`, confirm both containers are attached, confirm the upstream is `flowdraw:80`, then check both services' logs.

### HTTPS fails

Confirm that the Cloudflare record is proxied, SSL mode is **Full (strict)**, Caddy can obtain or load a valid certificate, the firewall permits the required origin traffic, and only the gateway publishes ports 80/443.

### Direct SPA routes return 404

The runtime config uses `try_files {path} /index.html`; rebuild/redeploy if an older image lacks it.

## Security notes

- Flowdraw's port 80 is internal only.
- PostgreSQL and MinIO are reachable only on the internal `backend` network.
- MinIO root credentials are administrative only. The API uses the dedicated `flowdraw-app` policy identity and cannot access other buckets. Never expose the MinIO console as a workaround.
- Room edit keys are capabilities: anyone with the complete invite link can edit that room.
- Its root filesystem is read-only with temporary `/data` and `/config`, plus `no-new-privileges`.
- The Docker socket is never mounted into Flowdraw.
- `VITE_*` values are compiled into public browser JavaScript. Never use them for passwords, tokens, private keys, or secrets.
- Future authentication, PostgreSQL, Redis, or sync services should be separate private services, not secrets baked into the frontend.
