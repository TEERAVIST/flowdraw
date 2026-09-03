import test from "node:test";
import assert from "node:assert/strict";

import {
  cleanFlowMetadata,
  inferJunctionTopology,
} from "../src/lib/topology.js";

const topology = (bindings) =>
  inferJunctionTopology(
    Object.fromEntries(
      bindings.map((flow, index) => [`arrow-${index}`, flow])
    ),
    "junction"
  );

test("infers split topology", () => {
  assert.deepEqual(
    topology([
      { endJunctionId: "junction" },
      { startJunctionId: "junction" },
      { startJunctionId: "junction" },
    ]),
    { incoming: 1, outgoing: 2, kind: "split" }
  );
});

test("infers merge topology", () => {
  assert.equal(
    topology([
      { endJunctionId: "junction" },
      { endJunctionId: "junction" },
      { startJunctionId: "junction" },
    ]).kind,
    "merge"
  );
});

test("ignores bindings to other junctions", () => {
  assert.deepEqual(
    topology([
      { endJunctionId: "other" },
      { startJunctionId: "junction" },
    ]),
    { incoming: 0, outgoing: 1, kind: "endpoint" }
  );
});

test("removes deleted arrows and missing junction bindings", () => {
  assert.deepEqual(
    cleanFlowMetadata(
      {
        live: {
          enabled: true,
          startJunctionId: "missing",
          endJunctionId: "kept",
        },
        deleted: { enabled: true },
      },
      [{ id: "live", type: "arrow", isDeleted: false }],
      [{ id: "kept" }]
    ),
    {
      live: {
        enabled: true,
        endJunctionId: "kept",
      },
    }
  );
});
