import { createHash, randomBytes, randomUUID, timingSafeEqual } from "node:crypto";
import { createServer } from "node:http";
import { Client as MinioClient } from "minio";
import pg from "pg";
import { WebSocketServer } from "ws";

const { Pool } = pg;
const port = Number(process.env.PORT || 3000);
const maxSnapshotBytes = Number(process.env.MAX_SNAPSHOT_BYTES || 5_000_000);
const maxObjectBytes = Number(process.env.MAX_OBJECT_BYTES || 25_000_000);
const bucket = process.env.S3_BUCKET || "flowdraw";
const pool = new Pool(process.env.DATABASE_URL
  ? { connectionString: process.env.DATABASE_URL }
  : {
      host: process.env.PGHOST || "postgres",
      port: Number(process.env.PGPORT || 5432),
      database: process.env.PGDATABASE || "flowdraw",
      user: process.env.PGUSER || "flowdraw",
      password: required("PGPASSWORD"),
    });
const storage = new MinioClient({
  endPoint: process.env.S3_ENDPOINT || "minio",
  port: Number(process.env.S3_PORT || 9000),
  useSSL: process.env.S3_USE_SSL === "true",
  accessKey: required("S3_ACCESS_KEY"),
  secretKey: required("S3_SECRET_KEY"),
});

function required(name) {
  const value = process.env[name];
  if (!value) throw new Error(`Missing required environment variable ${name}`);
  return value;
}

function tokenHash(token) {
  return createHash("sha256").update(token).digest();
}

function tokenMatches(token, expected) {
  const actual = tokenHash(token || "");
  return actual.length === expected.length && timingSafeEqual(actual, expected);
}

function json(response, status, body) {
  response.writeHead(status, { "content-type": "application/json; charset=utf-8", "cache-control": "no-store" });
  response.end(JSON.stringify(body));
}

async function readBody(request, limit = maxSnapshotBytes) {
  const chunks = [];
  let length = 0;
  for await (const chunk of request) {
    length += chunk.length;
    if (length > limit) throw Object.assign(new Error("Request is too large"), { status: 413 });
    chunks.push(chunk);
  }
  return Buffer.concat(chunks);
}

function bearer(request) {
  const value = request.headers.authorization || "";
  return value.startsWith("Bearer ") ? value.slice(7) : "";
}

async function authorizeRoom(roomId, token) {
  const result = await pool.query("SELECT edit_token_hash FROM rooms WHERE id = $1", [roomId]);
  return result.rowCount === 1 && tokenMatches(token, result.rows[0].edit_token_hash);
}

async function initialize() {
  await pool.query(`
    CREATE TABLE IF NOT EXISTS rooms (
      id uuid PRIMARY KEY,
      edit_token_hash bytea NOT NULL,
      snapshot jsonb NOT NULL DEFAULT '{}'::jsonb,
      revision bigint NOT NULL DEFAULT 0,
      created_at timestamptz NOT NULL DEFAULT now(),
      updated_at timestamptz NOT NULL DEFAULT now()
    );
    CREATE TABLE IF NOT EXISTS objects (
      id uuid PRIMARY KEY,
      room_id uuid NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
      object_key text NOT NULL UNIQUE,
      content_type text NOT NULL,
      byte_size bigint NOT NULL,
      created_at timestamptz NOT NULL DEFAULT now()
    );
  `);
  if (!(await storage.bucketExists(bucket))) {
    throw new Error(`Required object-storage bucket ${bucket} does not exist`);
  }
}

async function readiness() {
  try {
    const [, bucketAvailable] = await Promise.all([
      pool.query("SELECT 1"),
      storage.bucketExists(bucket),
    ]);
    return bucketAvailable;
  } catch {
    return false;
  }
}

const server = createServer(async (request, response) => {
  try {
    const url = new URL(request.url, "http://localhost");
    if (["/health/live", "/api/health/live"].includes(url.pathname)) {
      return json(response, 200, { status: "alive" });
    }
    if (["/health/ready", "/api/health/ready", "/api/health"].includes(url.pathname)) {
      return (await readiness())
        ? json(response, 200, { status: "ready" })
        : json(response, 503, { status: "not ready" });
    }

    if (request.method === "POST" && url.pathname === "/api/rooms") {
      const id = randomUUID();
      const token = randomBytes(32).toString("base64url");
      await pool.query("INSERT INTO rooms (id, edit_token_hash) VALUES ($1, $2)", [id, tokenHash(token)]);
      return json(response, 201, { id, token, revision: 0 });
    }

    const roomMatch = url.pathname.match(/^\/api\/rooms\/([0-9a-f-]+)$/i);
    if (request.method === "GET" && roomMatch) {
      const result = await pool.query("SELECT edit_token_hash, snapshot, revision FROM rooms WHERE id = $1", [roomMatch[1]]);
      if (!result.rowCount || !tokenMatches(bearer(request), result.rows[0].edit_token_hash)) return json(response, 404, { error: "Room not found" });
      return json(response, 200, { snapshot: result.rows[0].snapshot, revision: Number(result.rows[0].revision) });
    }

    const objectMatch = url.pathname.match(/^\/api\/rooms\/([0-9a-f-]+)\/objects(?:\/([0-9a-f-]+))?$/i);
    if (objectMatch && !(await authorizeRoom(objectMatch[1], bearer(request)))) return json(response, 404, { error: "Room not found" });
    if (request.method === "POST" && objectMatch && !objectMatch[2]) {
      const body = await readBody(request, maxObjectBytes);
      const id = randomUUID();
      const key = `rooms/${objectMatch[1]}/${id}`;
      const contentType = String(request.headers["content-type"] || "application/octet-stream").slice(0, 255);
      await storage.putObject(bucket, key, body, body.length, { "Content-Type": contentType });
      await pool.query("INSERT INTO objects (id, room_id, object_key, content_type, byte_size) VALUES ($1,$2,$3,$4,$5)", [id, objectMatch[1], key, contentType, body.length]);
      return json(response, 201, { id, contentType, size: body.length });
    }
    if (request.method === "GET" && objectMatch?.[2]) {
      const result = await pool.query("SELECT object_key, content_type, byte_size FROM objects WHERE id=$1 AND room_id=$2", [objectMatch[2], objectMatch[1]]);
      if (!result.rowCount) return json(response, 404, { error: "Object not found" });
      response.writeHead(200, { "content-type": result.rows[0].content_type, "content-length": result.rows[0].byte_size, "cache-control": "private, max-age=3600" });
      return (await storage.getObject(bucket, result.rows[0].object_key)).pipe(response);
    }

    json(response, 404, { error: "Not found" });
  } catch (error) {
    console.error(error);
    json(response, error.status || 500, { error: error.status ? error.message : "Internal server error" });
  }
});

const sockets = new WebSocketServer({ noServer: true, maxPayload: maxSnapshotBytes });
const roomClients = new Map();

server.on("upgrade", async (request, socket, head) => {
  try {
    const url = new URL(request.url, "http://localhost");
    const roomId = url.searchParams.get("room");
    if (
      url.pathname !== "/ws" ||
      !/^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i.test(roomId || "")
    ) return socket.destroy();
    request.roomId = roomId;
    sockets.handleUpgrade(request, socket, head, (websocket) => sockets.emit("connection", websocket, request));
  } catch (error) {
    console.error(error);
    socket.destroy();
  }
});

sockets.on("connection", async (socket, request) => {
  const roomId = request.roomId;
  let clients;
  let authenticated = false;
  let authenticationPending = false;
  const authenticationTimer = setTimeout(
    () => socket.close(1008, "Authentication required"),
    5000,
  );

  socket.on("message", async (raw) => {
    try {
      const message = JSON.parse(raw.toString());
      if (!authenticated) {
        if (
          authenticationPending ||
          message.type !== "authenticate" ||
          typeof message.token !== "string"
        ) {
          return socket.close(1008, "Authentication required");
        }
        authenticationPending = true;
        if (!(await authorizeRoom(roomId, message.token))) {
          return socket.close(1008, "Authentication failed");
        }
        authenticated = true;
        clearTimeout(authenticationTimer);
        clients = roomClients.get(roomId) || new Set();
        clients.add(socket);
        roomClients.set(roomId, clients);
        const current = await pool.query("SELECT snapshot, revision FROM rooms WHERE id=$1", [roomId]);
        socket.send(JSON.stringify({
          type: "snapshot",
          snapshot: current.rows[0].snapshot,
          revision: Number(current.rows[0].revision),
        }));
        return;
      }
      if (message.type === "presence") {
        for (const peer of clients) if (peer !== socket && peer.readyState === 1) peer.send(JSON.stringify(message));
        return;
      }
      if (message.type !== "snapshot" || !message.snapshot || typeof message.baseRevision !== "number") return;
      const encoded = JSON.stringify(message.snapshot);
      if (Buffer.byteLength(encoded) > maxSnapshotBytes) return socket.send(JSON.stringify({ type: "error", error: "Snapshot is too large" }));
      const result = await pool.query("UPDATE rooms SET snapshot=$1, revision=revision+1, updated_at=now() WHERE id=$2 AND revision=$3 RETURNING revision", [message.snapshot, roomId, message.baseRevision]);
      if (!result.rowCount) {
        const fresh = await pool.query("SELECT snapshot, revision FROM rooms WHERE id=$1", [roomId]);
        return socket.send(JSON.stringify({ type: "snapshot", snapshot: fresh.rows[0].snapshot, revision: Number(fresh.rows[0].revision), conflict: true }));
      }
      const outgoing = JSON.stringify({ type: "snapshot", snapshot: message.snapshot, revision: Number(result.rows[0].revision) });
      for (const peer of clients) if (peer.readyState === 1) peer.send(outgoing);
    } catch (error) {
      console.error(error);
    }
  });
  socket.on("close", () => {
    clearTimeout(authenticationTimer);
    clients?.delete(socket);
    if (clients && !clients.size) roomClients.delete(roomId);
  });
});

initialize().then(() => server.listen(port, "0.0.0.0", () => console.log(`Flowdraw API listening on ${port}`))).catch((error) => {
  console.error(error);
  process.exit(1);
});

async function shutdown() {
  sockets.close();
  server.close();
  await pool.end();
}
process.on("SIGTERM", shutdown);
process.on("SIGINT", shutdown);
