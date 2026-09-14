import assert from "node:assert/strict";
import test from "node:test";
import {
  readRoomCredentials,
  writeRoomCredentials,
} from "../src/lib/collaboration.js";

function setBrowserUrl(value) {
  globalThis.window = {
    location: new URL(value),
    history: {
      replaceState(_state, _title, nextUrl) {
        globalThis.window.location = new URL(nextUrl);
      },
    },
  };
}

test("writes collaboration credentials only to the URL fragment", () => {
  setBrowserUrl("https://flowdraw.example.com/?keep=yes");
  writeRoomCredentials({ roomId: "room-id", token: "private-key" });

  assert.equal(window.location.search, "?keep=yes");
  assert.equal(window.location.hash, "#room=room-id&key=private-key");
  assert.deepEqual(readRoomCredentials(), {
    roomId: "room-id",
    token: "private-key",
  });
});

test("migrates a legacy query link to a fragment", () => {
  setBrowserUrl("https://flowdraw.example.com/?room=legacy-room&key=legacy-key");
  assert.deepEqual(readRoomCredentials(), {
    roomId: "legacy-room",
    token: "legacy-key",
  });
  assert.equal(window.location.search, "");
  assert.equal(window.location.hash, "#room=legacy-room&key=legacy-key");
});
