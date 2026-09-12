# Flowdraw production deployment

## Architecture

GitHub Actions runs pnpm checks, builds the Docker image, pushes immutable `sha-<commit>` tags to GHCR, and deploys that exact image over SSH. The VPS only pulls and runs images. A separately managed Caddy gateway owns ports 80/443 and reaches Flowdraw through the external `proxy` Docker network.

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

No application publishes a host port. PostgreSQL and Redis are intentionally not included.

## A. One-time VPS setup

Connect to Ubuntu as the initial administrator:

```sh
ssh root@YOUR_VPS_IP
apt-get update
apt-get upgrade -y
```

Configure the Hetzner firewall for SSH, TCP 80, TCP 443, and UDP 443. Restrict SSH by source IP when practical.

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
```

Create `/opt/apps/flowdraw/.env` on the VPS:

```dotenv
GHCR_OWNER=your-github-username-in-lowercase
IMAGE_TAG=sha-full-git-commit
```

GitHub Actions rewrites these two non-secret values for each deployment, so production does not depend on `latest` and later Compose commands resolve the deployed image consistently. Never commit the real `.env`.

## G. Configure Caddy

Edit `/opt/apps/gateway/Caddyfile`:

```caddyfile
flowdraw.example.com {
    reverse_proxy flowdraw:80
}
```

Then start the shared gateway:

```sh
cd /opt/apps/gateway
docker compose up -d
docker compose ps
```

Caddy is the only service publishing 80/443 and automatically manages HTTPS after DNS is correct.

## H. Configure DNS

Create an `A` record for the selected hostname pointing to the VPS IPv4 address. Add `AAAA` only when IPv6 is configured. Wait for DNS propagation before diagnosing certificate issuance.

## I. Configure GitHub Secrets

Under **Repository Settings → Secrets and variables → Actions**, create:

- `VPS_HOST`: VPS hostname or IP.
- `VPS_USER`: normally `deploy`.
- `VPS_SSH_KEY`: private deployment key including BEGIN/END lines.
- `VPS_PORT`: normally `22`.
- `VPS_KNOWN_HOSTS`: verified SSH host-key line.

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

Push to `main`. The workflow installs from the frozen pnpm lockfile, runs lint/test/build, builds and pushes `sha-COMMIT`, deploys it with `--no-build`, and waits for a healthy container.

For a manual first start after setting `.env`:

```sh
cd /opt/apps/flowdraw
docker compose pull
docker compose up -d --no-build
docker compose ps
```

The VPS must not run `git pull`, `pnpm install`, `pnpm build`, `docker build`, or `docker compose build` during deployment.

## L. Normal deployments

Push to `main`; pull requests only run checks. The deploy job writes `GHCR_OWNER` and `IMAGE_TAG=sha-${GITHUB_SHA}` to `/opt/apps/flowdraw/.env` before pulling, guaranteeing the exact image created by that workflow run.

## M. Status and logs

```sh
cd /opt/apps/flowdraw
docker compose ps
docker compose logs --tail=200 flowdraw
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

Check DNS, firewall access to 80/443, and that only the gateway publishes those ports.

### Direct SPA routes return 404

The runtime config uses `try_files {path} /index.html`; rebuild/redeploy if an older image lacks it.

## Security notes

- Flowdraw's port 80 is internal only.
- Its root filesystem is read-only with temporary `/data` and `/config`, plus `no-new-privileges`.
- The Docker socket is never mounted into Flowdraw.
- `VITE_*` values are compiled into public browser JavaScript. Never use them for passwords, tokens, private keys, or secrets.
- Future authentication, PostgreSQL, Redis, or sync services should be separate private services, not secrets baked into the frontend.
