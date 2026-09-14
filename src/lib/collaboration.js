export function readRoomCredentials() {
  const fragment = window.location.hash.startsWith("#")
    ? window.location.hash.slice(1)
    : window.location.hash;
  const params = new URLSearchParams(fragment);
  const roomId = params.get("room");
  const token = params.get("key");
  if (roomId && token) return { roomId, token };

  // One-time compatibility migration for links created by older versions.
  const legacy = new URLSearchParams(window.location.search);
  const legacyRoomId = legacy.get("room");
  const legacyToken = legacy.get("key");
  if (!legacyRoomId || !legacyToken) return null;
  const credentials = { roomId: legacyRoomId, token: legacyToken };
  writeRoomCredentials(credentials);
  return credentials;
}

export function writeRoomCredentials(credentials) {
  const url = new URL(window.location.href);
  url.searchParams.delete("room");
  url.searchParams.delete("key");
  url.hash = new URLSearchParams({
    room: credentials.roomId,
    key: credentials.token,
  }).toString();
  window.history.replaceState(null, "", url);
}

export async function createCollaborationRoom() {
  const response = await fetch("/api/rooms", { method: "POST" });
  if (!response.ok) throw new Error("Could not create a collaboration room");
  const room = await response.json();
  return { roomId: room.id, token: room.token };
}

function authorization(credentials) {
  return { Authorization: `Bearer ${credentials.token}` };
}

export async function uploadRoomObject(credentials, file) {
  const response = await fetch(`/api/rooms/${credentials.roomId}/objects`, {
    method: "POST",
    headers: { ...authorization(credentials), "Content-Type": file.mimeType || "application/octet-stream" },
    body: await (await fetch(file.dataURL)).blob(),
  });
  if (!response.ok) throw new Error("Could not upload a collaboration asset");
  const object = await response.json();
  return {
    fileId: file.id,
    objectId: object.id,
    mimeType: file.mimeType,
    created: file.created,
    lastRetrieved: file.lastRetrieved,
  };
}

export async function downloadRoomObject(credentials, reference) {
  const response = await fetch(
    `/api/rooms/${credentials.roomId}/objects/${reference.objectId}`,
    { headers: authorization(credentials) },
  );
  if (!response.ok) throw new Error("Could not download a collaboration asset");
  const blob = await response.blob();
  const dataURL = await new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => resolve(reader.result);
    reader.onerror = reject;
    reader.readAsDataURL(blob);
  });
  return { ...reference, id: reference.fileId, dataURL };
}

export function connectToRoom(credentials, handlers) {
  let revision = 0;
  let ready = false;
  let closed = false;
  let retryTimer;
  let socket;

  const open = () => {
    if (closed) return;
    handlers.onStatus("connecting");
    const protocol = window.location.protocol === "https:" ? "wss:" : "ws:";
    const url = new URL("/ws", `${protocol}//${window.location.host}`);
    url.searchParams.set("room", credentials.roomId);
    socket = new WebSocket(url);
    socket.addEventListener("open", () => {
      socket.send(JSON.stringify({
        type: "authenticate",
        token: credentials.token,
      }));
    });
    socket.addEventListener("message", (event) => {
      const message = JSON.parse(event.data);
      if (message.type !== "snapshot") return;
      revision = message.revision;
      ready = true;
      handlers.onSnapshot(message.snapshot, message.conflict === true);
      handlers.onStatus("connected");
    });
    socket.addEventListener("close", () => {
      ready = false;
      if (!closed) {
        handlers.onStatus("offline");
        retryTimer = window.setTimeout(open, 1500);
      }
    });
    socket.addEventListener("error", () => socket.close());
  };

  open();
  return {
    send(snapshot) {
      if (!ready || socket?.readyState !== WebSocket.OPEN) return false;
      socket.send(JSON.stringify({ type: "snapshot", baseRevision: revision, snapshot }));
      return true;
    },
    close() {
      closed = true;
      clearTimeout(retryTimer);
      socket?.close();
    },
  };
}
