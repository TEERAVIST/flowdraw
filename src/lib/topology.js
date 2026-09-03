export function inferJunctionTopology(flows, junctionId) {
  const flowValues = Object.values(flows);
  const incoming = flowValues.filter(
    (flow) => flow?.endJunctionId === junctionId
  ).length;
  const outgoing = flowValues.filter(
    (flow) => flow?.startJunctionId === junctionId
  ).length;

  let kind = "unconnected";

  if (incoming === 1 && outgoing === 2) {
    kind = "split";
  } else if (incoming === 2 && outgoing === 1) {
    kind = "merge";
  } else if (incoming === 1 && outgoing === 1) {
    kind = "pass-through";
  } else if (incoming > 1 && outgoing > 1) {
    kind = "general";
  } else if (incoming || outgoing) {
    kind = "endpoint";
  }

  return { incoming, outgoing, kind };
}

export function cleanFlowMetadata(flows, elements, junctions) {
  const activeArrowIds = new Set(
    elements
      .filter((element) =>
        !element.isDeleted && element.type === "arrow"
      )
      .map((element) => element.id)
  );
  const junctionIds = new Set(
    junctions.map((junction) => junction.id)
  );
  let changed = false;
  const next = {};

  for (const [arrowId, flow] of Object.entries(flows)) {
    if (!activeArrowIds.has(arrowId)) {
      changed = true;
      continue;
    }

    const cleaned = { ...flow };
    for (const endpoint of ["start", "end"]) {
      const key = `${endpoint}JunctionId`;
      if (cleaned[key] && !junctionIds.has(cleaned[key])) {
        delete cleaned[key];
        changed = true;
      }
    }
    next[arrowId] = cleaned;
  }

  return changed ? next : flows;
}
