#!/bin/sh
set -eu

compose_file="${COMPOSE_FILE:-compose.yml}"
minio_container="$(docker compose -f "$compose_file" ps -q minio)"

if [ -z "$minio_container" ]; then
  echo "MinIO is not running. Start it before provisioning." >&2
  exit 1
fi

docker run --rm \
  --network "container:$minio_container" \
  --env-file .env \
  -v "$(pwd)/minio-flowdraw-policy.json:/policy.json:ro" \
  --entrypoint /bin/sh \
  minio/mc:latest \
  -c '
    set -eu
    mc alias set local http://127.0.0.1:9000 "$MINIO_ROOT_USER" "$MINIO_ROOT_PASSWORD" >/dev/null
    mc mb --ignore-existing local/flowdraw >/dev/null
    mc admin policy create local flowdraw-app /policy.json >/dev/null
    if ! mc admin user info local "$FLOWDRAW_S3_ACCESS_KEY" >/dev/null 2>&1; then
      mc admin user add local "$FLOWDRAW_S3_ACCESS_KEY" "$FLOWDRAW_S3_SECRET_KEY" >/dev/null
    fi
    mc admin policy attach local flowdraw-app --user "$FLOWDRAW_S3_ACCESS_KEY" >/dev/null
    echo "Flowdraw MinIO identity and bucket are ready."
  '
