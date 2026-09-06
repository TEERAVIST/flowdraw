/* eslint-disable react-refresh/only-export-components */
import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
} from "react";

import { createRoot } from "react-dom/client";

import {
  Excalidraw,
  sceneCoordsToViewportCoords,
  viewportCoordsToSceneCoords,
  newElementWith,
} from "@excalidraw/excalidraw";

import "@excalidraw/excalidraw/index.css";
import "./flowdraw.css";

import {
  cleanFlowMetadata,
  inferJunctionTopology,
} from "./lib/topology.js";
import { FlowPatternSelect } from "./components/FlowPatternSelect.jsx";


/* =========================================================
   CONFIG
========================================================= */

const SCENE_STORAGE_KEY = "flowdraw.scene.v2";
const FLOW_STORAGE_KEY = "flowdraw.flows.v2";
const JUNCTION_STORAGE_KEY = "flowdraw.junctions.v1";
const PANEL_STORAGE_KEY = "flowdraw.panel.v1";
const PACKET_STORAGE_KEY = "flowdraw.packets.v1";

const DEFAULT_SPEED = 120;


/* =========================================================
   STORAGE
========================================================= */

function loadSavedScene() {
  try {
    const raw = localStorage.getItem(
      SCENE_STORAGE_KEY
    );

    if (!raw) {
      return {
        elements: [],
        appState: {
          viewBackgroundColor: "#ffffff",
        },
      };
    }

    const saved = JSON.parse(raw);

    if (!Array.isArray(saved?.elements)) {
      throw new Error("Invalid saved scene format");
    }

    return {
      elements: saved.elements,
      appState: {
        viewBackgroundColor: "#ffffff",
      },
    };
  } catch (error) {
    console.error(
      "Failed to load scene:",
      error
    );

    return {
      elements: [],
      appState: {
        viewBackgroundColor: "#ffffff",
      },
    };
  }
}


function loadSavedFlows() {
  try {
    const raw = localStorage.getItem(
      FLOW_STORAGE_KEY
    );

    if (!raw) {
      return {};
    }

    const saved = JSON.parse(raw);

    if (
      !saved ||
      typeof saved !== "object" ||
      Array.isArray(saved)
    ) {
      throw new Error("Invalid saved flow format");
    }

    return Object.fromEntries(
      Object.entries(saved).filter(
        ([arrowId, flow]) =>
          typeof arrowId === "string" &&
          flow &&
          typeof flow === "object"
      )
    );
  } catch (error) {
    console.error(
      "Failed to load flows:",
      error
    );

    return {};
  }
}


function loadSavedJunctions() {
  try {
    const raw = localStorage.getItem(
      JUNCTION_STORAGE_KEY
    );

    if (!raw) {
      return [];
    }

    const saved = JSON.parse(raw);

    if (!Array.isArray(saved)) {
      throw new Error("Invalid saved junction format");
    }

    return saved
      .filter((junction) =>
        junction &&
        typeof junction.id === "string" &&
        Number.isFinite(junction.x) &&
        Number.isFinite(junction.y)
      )
      .map((junction) => ({
        id: junction.id,
        x: junction.x,
        y: junction.y,
        visible: junction.visible !== false,
        size: Number.isFinite(junction.size)
          ? junction.size
          : 6,
        color: typeof junction.color === "string"
          ? junction.color
          : "#1b1b1f",
      }));
  } catch (error) {
    console.error(
      "Failed to load junctions:",
      error
    );

    return [];
  }
}


function createJunctionId() {
  return globalThis.crypto?.randomUUID?.() ??
    `junction-${Date.now()}-${Math.random()
      .toString(36)
      .slice(2)}`;
}


function loadPanelState() {
  const fallback = {
    x: Math.max(12, window.innerWidth - 228),
    y: 80,
    collapsed: false,
  };

  try {
    const saved = JSON.parse(
      localStorage.getItem(PANEL_STORAGE_KEY)
    );

    return {
      x: Number.isFinite(saved?.x) ? saved.x : fallback.x,
      y: Number.isFinite(saved?.y) ? saved.y : fallback.y,
      collapsed: saved?.collapsed === true,
    };
  } catch {
    return fallback;
  }
}


function loadSavedPackets() {
  try {
    const saved = JSON.parse(
      localStorage.getItem(PACKET_STORAGE_KEY) ?? "[]"
    );

    return Array.isArray(saved)
      ? saved.filter((packet) =>
          packet &&
          typeof packet.id === "string" &&
          typeof packet.arrowId === "string"
        ).map((packet) => ({
          id: packet.id,
          arrowId: packet.arrowId,
          label: typeof packet.label === "string"
            ? packet.label
            : "Packet",
          type: typeof packet.type === "string"
            ? packet.type
            : "data",
          status: ["queued", "moving", "delivered", "failed"]
            .includes(packet.status)
            ? packet.status
            : "moving",
          color: typeof packet.color === "string"
            ? packet.color
            : "#4c6ef5",
          payload: typeof packet.payload === "string"
            ? packet.payload
            : JSON.stringify(packet.payload ?? {}, null, 2),
        }))
      : [];
  } catch {
    return [];
  }
}


function getOverlayAppState(appState) {
  return {
    zoom: appState.zoom,
    offsetLeft: appState.offsetLeft,
    offsetTop: appState.offsetTop,
    scrollX: appState.scrollX,
    scrollY: appState.scrollY,
    selectedElementIds: appState.selectedElementIds ?? {},
  };
}


function formatPacketPayload(payload) {
  try {
    return JSON.stringify(JSON.parse(payload), null, 2);
  } catch {
    return payload || "(empty payload)";
  }
}


/* =========================================================
   GEOMETRY
========================================================= */

function rotatePoint(
  x,
  y,
  centerX,
  centerY,
  angle
) {
  if (!angle) {
    return { x, y };
  }

  const cos = Math.cos(angle);
  const sin = Math.sin(angle);

  const dx = x - centerX;
  const dy = y - centerY;

  return {
    x:
      centerX +
      dx * cos -
      dy * sin,

    y:
      centerY +
      dx * sin +
      dy * cos,
  };
}


/*
  Excalidraw linear element:

  element.x / element.y
       +
  element.points[]

  points เป็น local coordinates
*/
function getArrowScenePoints(element) {
  if (
    !element.points ||
    element.points.length < 2
  ) {
    return [];
  }

  const centerX =
    element.x + element.width / 2;

  const centerY =
    element.y + element.height / 2;

  return element.points.map(
    ([pointX, pointY]) => {
      const sceneX =
        element.x + pointX;

      const sceneY =
        element.y + pointY;

      return rotatePoint(
        sceneX,
        sceneY,
        centerX,
        centerY,
        element.angle ?? 0
      );
    }
  );
}


function getViewportPoints(
  element,
  appState,
  containerRect
) {
  const scenePoints =
    getArrowScenePoints(element);

  return scenePoints.map(
    ({ x, y }) => {

      const viewport =
        sceneCoordsToViewportCoords(
          {
            sceneX: x,
            sceneY: y,
          },
          appState
        );

      /*
        utility คืน viewport/client coordinate

        overlay SVG ของเราเริ่มจาก
        top-left ของ wrapper

        จึงลบตำแหน่ง wrapper ออก
      */
      return {
        x:
          viewport.x -
          containerRect.left,

        y:
          viewport.y -
          containerRect.top,
      };
    }
  );
}


function pointsToPath(points) {
  if (!points || points.length < 2) {
    return "";
  }

  let result =
    `M ${points[0].x} ${points[0].y}`;

  for (
    let i = 1;
    i < points.length;
    i++
  ) {
    result +=
      ` L ${points[i].x} ${points[i].y}`;
  }

  return result;
}


function moveArrowEndpoint(
  element,
  endpoint,
  sceneX,
  sceneY
) {
  if (
    element.type !== "arrow" ||
    !element.points?.length
  ) {
    return element;
  }

  const originalScenePoints =
    getArrowScenePoints(element);
  const originalIndex = endpoint === "start"
    ? 0
    : originalScenePoints.length - 1;

  if (
    Math.hypot(
      sceneX - originalScenePoints[originalIndex].x,
      sceneY - originalScenePoints[originalIndex].y
    ) < 0.05
  ) {
    return element;
  }

  let candidate = {
    ...element,
    points: element.points.map(
    ([x, y]) => [x, y]
    ),
  };

  // The rotation centre changes as a linear element's bounds change.
  // Iterating removes that moving-centre error and also works for elbows.
  for (let iteration = 0; iteration < 5; iteration++) {
    const scenePoints = getArrowScenePoints(candidate);
    const index = endpoint === "start"
      ? 0
      : scenePoints.length - 1;
    const current = scenePoints[index];
    const errorX = sceneX - current.x;
    const errorY = sceneY - current.y;

    if (Math.hypot(errorX, errorY) < 0.01) {
      break;
    }

    const angle = -(candidate.angle ?? 0);
    const localDx =
      errorX * Math.cos(angle) -
      errorY * Math.sin(angle);
    const localDy =
      errorX * Math.sin(angle) +
      errorY * Math.cos(angle);

    if (endpoint === "start") {
      candidate.x += localDx;
      candidate.y += localDy;
      candidate.points = candidate.points.map(
        ([x, y], pointIndex) =>
          pointIndex === 0
            ? [0, 0]
            : [x - localDx, y - localDy]
      );
    } else {
      const last = candidate.points.length - 1;
      candidate.points[last] = [
        candidate.points[last][0] + localDx,
        candidate.points[last][1] + localDy,
      ];
    }

    const xs = candidate.points.map(([x]) => x);
    const ys = candidate.points.map(([, y]) => y);
    candidate.width = Math.max(...xs) - Math.min(...xs);
    candidate.height = Math.max(...ys) - Math.min(...ys);
  }

  return newElementWith(element, {
    x: candidate.x,
    y: candidate.y,
    points: candidate.points,
    width: candidate.width,
    height: candidate.height,
  });
}


function applyJunctionBindings(
  elements,
  flows,
  junctions
) {
  const junctionMap = new Map(
    junctions.map((junction) => [junction.id, junction])
  );
  let changed = false;

  const next = elements.map((element) => {
    const flow = flows[element.id];
    let updated = element;

    for (const endpoint of ["start", "end"]) {
      const junctionId =
        flow?.[`${endpoint}JunctionId`];
      const junction = junctionMap.get(junctionId);

      if (junction) {
        updated = moveArrowEndpoint(
          updated,
          endpoint,
          junction.x,
          junction.y
        );
      }
    }

    changed ||= updated !== element;
    return updated;
  });

  return { elements: next, changed };
}


/* =========================================================
   FLOW SVG
========================================================= */

function AnimatedArrow({
  element,
  flow,
  packets,
  paused,
  appState,
  containerRect,
  selectedPacketId,
  onPacketSelect,
}) {
  const animationSvgRef = useRef(null);

  useEffect(() => {
    const animationSvg = animationSvgRef.current;
    if (!animationSvg) {
      return;
    }

    if (paused) {
      animationSvg.pauseAnimations();
    } else {
      animationSvg.unpauseAnimations();
    }
  }, [paused]);

  const points =
    getViewportPoints(
      element,
      appState,
      containerRect
    );

  const d = pointsToPath(points);

  if (!d) {
    return null;
  }

  const pathLength = points.reduce(
    (total, point, index) =>
      index === 0
        ? total
        : total + Math.hypot(
            point.x - points[index - 1].x,
            point.y - points[index - 1].y
          ),
    0
  );

  const zoom =
    appState.zoom?.value ?? 1;

  const speed =
    Number(flow.speed) ||
    DEFAULT_SPEED;

  const direction =
    flow.direction === -1
      ? -1
      : 1;

  const pattern =
    flow.pattern ?? "dashes";


  /*
    pattern size
  */
  const dash =
    pattern === "dots"
      ? 2 * zoom
      : 13 * zoom;
  const gap =
    pattern === "dots"
      ? 12 * zoom
      : 9 * zoom;

  const cycle = dash + gap;


  /*
    pixel / second

    สมมติ cycle 22px
    speed 120 px/s

    duration ≈ 0.18s
  */
  const duration =
    Math.max(
      0.12,
      cycle / speed
    );

  const lumpDuration = Math.max(
    0.8,
    pathLength / speed
  );


  const strokeWidth =
    Math.max(
      2,
      (element.strokeWidth ?? 2) *
        zoom
    );


  const pathId =
    `flow-path-${element.id}`
      .replace(
        /[^a-zA-Z0-9_-]/g,
        ""
      );


  const dashOffset =
    direction === 1
      ? -cycle
      : cycle;


  const arrowMarkerId =
  `arrow-head-${element.id}`
    .replace(/[^a-zA-Z0-9_-]/g, "");

  return (
    <svg
      ref={animationSvgRef}
      className="flow-animation-clock"
      x="0"
      y="0"
      width="100%"
      height="100%"
      overflow="visible"
    >

      <defs>
  <marker
    id={arrowMarkerId}
    viewBox="0 0 12 12"
    markerWidth={12 * zoom}
    markerHeight={12 * zoom}
    refX="10"
    refY="6"
    orient="auto-start-reverse" 
    markerUnits="userSpaceOnUse"
  >
    <path
      d="M 1 1 L 11 6 L 1 11 Z"
      fill={
        element.strokeColor ||
        "#1b1b1f"
      }
    />
  </marker>
</defs>

      {/* invisible motion path */}
      <path
        id={pathId}
        d={d}
        fill="none"
        stroke="transparent"
        strokeWidth="1"
      />


      {pattern === "lumps" ? (
        <>
          <path
            d={d}
            fill="none"
            stroke={
              element.strokeColor ||
              "#1b1b1f"
            }
            strokeOpacity="0.32"
            strokeWidth={Math.max(1.5, strokeWidth * 0.65)}
            strokeLinecap="round"
            strokeLinejoin="round"
            markerStart={
              direction === -1 &&
              !flow.startJunctionId
                ? `url(#${arrowMarkerId})`
                : undefined
            }
            markerEnd={
              direction === 1 &&
              !flow.endJunctionId
                ? `url(#${arrowMarkerId})`
                : undefined
            }
          />

          {(packets.length
            ? packets
            : [0, 1, 2].map((index) => ({
                id: `decorative-${index}`,
                decorative: true,
              }))
          ).map((packet, index, packetList) => (
            <g
              key={packet.id}
              className={
                packet.decorative
                  ? ""
                  : "flow-packet"
              }
              role={packet.decorative ? undefined : "button"}
              tabIndex={packet.decorative ? undefined : 0}
              aria-label={packet.decorative
                ? undefined
                : `Open payload for ${packet.label || "packet"}`}
              onPointerDown={packet.decorative
                ? undefined
                : (event) => {
                    event.preventDefault();
                    event.stopPropagation();
                    onPacketSelect(packet.id, {
                      clientX: event.clientX,
                      clientY: event.clientY,
                    });
                  }}
              onClick={packet.decorative
                ? undefined
                : (event) => {
                    event.preventDefault();
                    event.stopPropagation();
                    onPacketSelect(packet.id, {
                      clientX: event.clientX,
                      clientY: event.clientY,
                    });
                  }}
              onKeyDown={packet.decorative
                ? undefined
                : (event) => {
                    if (event.key === "Enter" || event.key === " ") {
                      event.preventDefault();
                      onPacketSelect(packet.id);
                    }
                  }}
            >
              {packet.decorative ? (
                <circle
                  r={Math.max(4, strokeWidth * 1.45)}
                  fill={element.strokeColor || "#1b1b1f"}
                />
              ) : (
                <>
                  <rect
                    x={-Math.max(28, packet.label.length * 3.8 + 12)}
                    y="-11"
                    width={Math.max(56, packet.label.length * 7.6 + 24)}
                    height="22"
                    rx="11"
                    fill={packet.color || "#4c6ef5"}
                    stroke={
                      selectedPacketId === packet.id
                        ? "#ffd43b"
                        : "#ffffff"
                    }
                    strokeWidth="2"
                  />
                  <text
                    textAnchor="middle"
                    dominantBaseline="central"
                    fill="#ffffff"
                    fontSize="11"
                    fontFamily="Arial, sans-serif"
                  >
                    {packet.label || "Packet"}
                  </text>
                </>
              )}
              <animateMotion
                dur={`${lumpDuration}s`}
                begin={`${-(index * lumpDuration) / packetList.length}s`}
                repeatCount="indefinite"
                keyPoints={
                  direction === 1
                    ? "0;1"
                    : "1;0"
                }
                keyTimes="0;1"
                calcMode="linear"
              >
                <mpath href={`#${pathId}`} />
              </animateMotion>
            </g>
          ))}
        </>
      ) : (
        <path
          d={d}
          fill="none"
          markerStart={
            direction === -1 &&
            !flow.startJunctionId
              ? `url(#${arrowMarkerId})`
              : undefined
          }
          markerEnd={
            direction === 1 &&
            !flow.endJunctionId
              ? `url(#${arrowMarkerId})`
              : undefined
          }
          stroke={
            element.strokeColor ||
            "#1b1b1f"
          }
          strokeWidth={strokeWidth}
          strokeLinecap="round"
          strokeLinejoin="round"
          strokeDasharray={`${dash} ${gap}`}
        >
          <animate
            attributeName="stroke-dashoffset"
            from="0"
            to={dashOffset}
            dur={`${duration}s`}
            repeatCount="indefinite"
          />
        </path>
      )}

    </svg>
  );
}


/* =========================================================
   OVERLAY
========================================================= */

function FlowOverlay({
  elements,
  flows,
  junctions,
  packets,
  globalPaused,
  appState,
  containerRect,
  selectedJunctionId,
  onJunctionPointerDown,
  selectedPacketId,
  onPacketSelect,
}) {

  if (!appState) {
    return null;
  }

  if (
    containerRect.width <= 0 ||
    containerRect.height <= 0
  ) {
    return null;
  }


  const animatedArrows =
    elements.filter(
      (element) =>
        !element.isDeleted &&
        element.type === "arrow" &&
        flows[element.id]?.enabled
    );


  if (
    animatedArrows.length === 0 &&
    junctions.length === 0
  ) {
    return null;
  }


  return (
    <svg
      className="flow-overlay"

      width={
        containerRect.width
      }

      height={
        containerRect.height
      }

      viewBox={
        `0 0 ` +
        `${containerRect.width} ` +
        `${containerRect.height}`
      }
    >
      {animatedArrows.map(
        (element) => (
          <AnimatedArrow
            key={element.id}

            element={element}

            flow={
              flows[element.id]
            }

            packets={packets.filter(
              (packet) => packet.arrowId === element.id
            )}

            paused={
              globalPaused || flows[element.id]?.paused === true
            }

            appState={
              appState
            }

            containerRect={
              containerRect
            }

            selectedPacketId={selectedPacketId}

            onPacketSelect={onPacketSelect}
          />
        )
      )}

      {junctions
        .filter((junction) => junction.visible)
        .map((junction) => {
          const viewport =
            sceneCoordsToViewportCoords(
              {
                sceneX: junction.x,
                sceneY: junction.y,
              },
              appState
            );

          return (
            <circle
              key={junction.id}
              className="flow-junction"
              cx={viewport.x - containerRect.left}
              cy={viewport.y - containerRect.top}
              r={
                junction.id === selectedJunctionId
                  ? (junction.size ?? 6) + 2
                  : junction.size ?? 6
              }
              fill={junction.color ?? "#1b1b1f"}
              stroke={
                junction.id === selectedJunctionId
                  ? "#4c6ef5"
                  : "#ffffff"
              }
              strokeWidth="2"
              onPointerDown={(event) =>
                onJunctionPointerDown(event, junction.id)
              }
            />
          );
        })}
    </svg>
  );
}


/* =========================================================
   APP
========================================================= */

function App() {

  const wrapperRef =
    useRef(null);

  const panelRef =
    useRef(null);

  const saveTimerRef =
    useRef(null);

  const excalidrawApiRef =
    useRef(null);


  const setExcalidrawApi =
    useCallback((api) => {
      excalidrawApiRef.current = api;
    }, []);


  const initialData =
    useMemo(
      () => loadSavedScene(),
      []
    );


  const [elements, setElements] =
    useState(
      initialData.elements ?? []
    );


  const [appState, setAppState] =
    useState(null);


  const [flows, setFlows] =
    useState(
      () => loadSavedFlows()
    );


  const [junctions, setJunctions] =
    useState(
      () => loadSavedJunctions()
    );

  const [packets, setPackets] =
    useState(loadSavedPackets);

  const [selectedPacketId, setSelectedPacketId] =
    useState(null);

  const [packetPreview, setPacketPreview] =
    useState(null);

  const [globalPaused, setGlobalPaused] =
    useState(false);


  const [selectedJunctionId,
    setSelectedJunctionId] = useState(null);


  const [isPlacingJunction,
    setIsPlacingJunction] = useState(false);

  const [panelState, setPanelState] =
    useState(loadPanelState);

  const flowsRef = useRef(flows);
  const junctionsRef = useRef(junctions);

  useEffect(() => {
    flowsRef.current = flows;
  }, [flows]);

  useEffect(() => {
    junctionsRef.current = junctions;
  }, [junctions]);


  useEffect(() => {
    localStorage.setItem(
      PANEL_STORAGE_KEY,
      JSON.stringify(panelState)
    );
  }, [panelState]);


  useEffect(() => {
    localStorage.setItem(
      PACKET_STORAGE_KEY,
      JSON.stringify(packets)
    );
  }, [packets]);


  useEffect(() => {
    const keepPanelOnScreen = () => {
      setPanelState((previous) => ({
        ...previous,
        x: Math.min(
          Math.max(8, previous.x),
          Math.max(8, window.innerWidth - (previous.collapsed ? 64 : 228))
        ),
        y: Math.min(
          Math.max(8, previous.y),
          Math.max(8, window.innerHeight - 52)
        ),
      }));
    };

    window.addEventListener("resize", keepPanelOnScreen);
    return () => window.removeEventListener(
      "resize",
      keepPanelOnScreen
    );
  }, []);


  const [
    containerRect,
    setContainerRect,
  ] = useState({
    width: 0,
    height: 0,
    left: 0,
    top: 0,
  });


  /* -----------------------------------------------------
     Measure wrapper
  ----------------------------------------------------- */

  useEffect(() => {

    const wrapper =
      wrapperRef.current;

    if (!wrapper) {
      return;
    }


    const updateRect = () => {

      const rect =
        wrapper
          .getBoundingClientRect();

      setContainerRect({
        width:
          rect.width,

        height:
          rect.height,

        left:
          rect.left,

        top:
          rect.top,
      });
    };


    updateRect();


    const resizeObserver =
      new ResizeObserver(
        updateRect
      );


    resizeObserver.observe(
      wrapper
    );


    window.addEventListener(
      "resize",
      updateRect
    );


    return () => {

      resizeObserver.disconnect();

      window.removeEventListener(
        "resize",
        updateRect
      );
    };

  }, []);


  /* -----------------------------------------------------
     Save flows
  ----------------------------------------------------- */

  useEffect(() => {

    localStorage.setItem(
      FLOW_STORAGE_KEY,
      JSON.stringify(flows)
    );

  }, [flows]);


  useEffect(() => {
    localStorage.setItem(
      JUNCTION_STORAGE_KEY,
      JSON.stringify(junctions)
    );
  }, [junctions]);


  useEffect(() => () => {
    clearTimeout(saveTimerRef.current);
  }, []);


  /* -----------------------------------------------------
     Excalidraw onChange
  ----------------------------------------------------- */

  const handleChange =
    useCallback(
      (
        nextElements,
        nextAppState
      ) => {

        const array =
          Array.from(
            nextElements
          );

        const bound = applyJunctionBindings(
          array,
          flowsRef.current,
          junctionsRef.current
        );

        setFlows((previous) =>
          cleanFlowMetadata(
            previous,
            bound.elements,
            junctionsRef.current
          )
        );

        const activeArrowIds = new Set(
          bound.elements
            .filter((element) =>
              !element.isDeleted && element.type === "arrow"
            )
            .map((element) => element.id)
        );
        setPackets((previous) => {
          const next = previous.filter((packet) =>
            activeArrowIds.has(packet.arrowId)
          );
          return next.length === previous.length
            ? previous
            : next;
        });

        setElements(bound.elements);

        const overlayAppState =
          getOverlayAppState(nextAppState);

        setAppState((previous) => {
          if (
            previous &&
            previous.zoom?.value === overlayAppState.zoom?.value &&
            previous.offsetLeft === overlayAppState.offsetLeft &&
            previous.offsetTop === overlayAppState.offsetTop &&
            previous.scrollX === overlayAppState.scrollX &&
            previous.scrollY === overlayAppState.scrollY &&
            Object.keys(previous.selectedElementIds).length ===
              Object.keys(overlayAppState.selectedElementIds).length &&
            Object.keys(previous.selectedElementIds).every(
              (id) => overlayAppState.selectedElementIds[id]
            )
          ) {
            return previous;
          }

          return overlayAppState;
        });


        /*
          debounce scene save
        */
        clearTimeout(
          saveTimerRef.current
        );


        saveTimerRef.current =
          setTimeout(() => {

            try {

              localStorage.setItem(
                SCENE_STORAGE_KEY,

                JSON.stringify({
                  elements:
                    bound.elements,
                })
              );

            } catch (error) {

              console.error(
                "Autosave failed:",
                error
              );
            }

          }, 250);

        if (bound.changed) {
          queueMicrotask(() => {
            excalidrawApiRef.current?.updateScene({
              elements: bound.elements,
            });
          });
        }
      },
      []
    );


  /* -----------------------------------------------------
     Selected arrows
  ----------------------------------------------------- */

  const selectedArrowIds =
    useMemo(() => {

      if (!appState) {
        return [];
      }

      const selected =
        appState
          .selectedElementIds ??
        {};


      return elements
        .filter(
          (element) =>
            !element.isDeleted &&
            element.type ===
              "arrow" &&
            Boolean(
              selected[
                element.id
              ]
            )
        )
        .map(
          (element) =>
            element.id
        );

    }, [
      elements,
      appState,
    ]);


  const hasSelectedArrow =
    selectedArrowIds.length > 0;


  const allSelectedAreOn =
    hasSelectedArrow &&
    selectedArrowIds.every(
      (id) =>
        flows[id]?.enabled
    );

  const allSelectedArePaused =
    hasSelectedArrow &&
    selectedArrowIds.every(
      (id) => flows[id]?.paused === true
    );


  const selectedFlow =
    selectedArrowIds.length
      ? flows[
          selectedArrowIds[0]
        ]
      : null;


  const displayedSpeed =
    selectedFlow?.speed ??
    DEFAULT_SPEED;


  const displayedPattern =
    selectedFlow?.pattern ??
    "dashes";


  const selectedJunctionTopology =
    useMemo(() => {
      if (!selectedJunctionId) {
        return null;
      }

      return inferJunctionTopology(
        flows,
        selectedJunctionId
      );
    }, [flows, selectedJunctionId]);


  /* -----------------------------------------------------
     Flow toggle
  ----------------------------------------------------- */

  const toggleFlow =
    useCallback(() => {

      if (
        selectedArrowIds.length ===
        0
      ) {
        return;
      }


      setFlows(
        (previous) => {

          const next = {
            ...previous,
          };


          /*
            ถ้าทุกตัวเปิดอยู่แล้ว
            -> OFF

            ถ้ามีตัวใดยัง OFF
            -> ON ทั้งหมด
          */
          const shouldEnable =
            !selectedArrowIds.every(
              (id) =>
                previous[id]
                  ?.enabled
            );


          for (
            const id
            of selectedArrowIds
          ) {

            next[id] = {
              ...previous[id],
              enabled:
                shouldEnable,

              speed:
                previous[id]
                  ?.speed ??
                DEFAULT_SPEED,

              direction:
                previous[id]
                  ?.direction ??
                1,
            };
          }


          return next;
        }
      );

    }, [selectedArrowIds]);


  /* -----------------------------------------------------
     Speed
  ----------------------------------------------------- */

  const setSpeed =
    useCallback(
      (speed) => {

        if (
          selectedArrowIds.length ===
          0
        ) {
          return;
        }


        setFlows(
          (previous) => {

            const next = {
              ...previous,
            };


            for (
              const id
              of selectedArrowIds
            ) {

              next[id] = {
                ...previous[id],
                enabled:
                  previous[id]
                    ?.enabled ??
                  true,

                speed,

                direction:
                  previous[id]
                    ?.direction ??
                  1,
              };
            }


            return next;
          }
        );

      },
      [selectedArrowIds]
    );


  const setPattern =
    useCallback(
      (pattern) => {
        if (!selectedArrowIds.length) {
          return;
        }

        setFlows((previous) => {
          const next = { ...previous };

          for (const id of selectedArrowIds) {
            next[id] = {
              ...previous[id],
              enabled: previous[id]?.enabled ?? true,
              speed: previous[id]?.speed ?? DEFAULT_SPEED,
              direction: previous[id]?.direction ?? 1,
              pattern,
            };
          }

          return next;
        });
      },
      [selectedArrowIds]
    );


  const toggleSelectedPause =
    useCallback(() => {
      if (!selectedArrowIds.length) {
        return;
      }

      setFlows((previous) => {
        const shouldPause = !selectedArrowIds.every(
          (id) => previous[id]?.paused === true
        );
        const next = { ...previous };

        for (const id of selectedArrowIds) {
          next[id] = {
            ...previous[id],
            enabled: previous[id]?.enabled ?? true,
            speed: previous[id]?.speed ?? DEFAULT_SPEED,
            direction: previous[id]?.direction ?? 1,
            pattern: previous[id]?.pattern ?? "dashes",
            paused: shouldPause,
          };
        }

        return next;
      });
    }, [selectedArrowIds]);


  /* -----------------------------------------------------
     Reverse
  ----------------------------------------------------- */

  const reverseDirection =
    useCallback(() => {

      if (
        selectedArrowIds.length ===
        0
      ) {
        return;
      }


      setFlows(
        (previous) => {

          const next = {
            ...previous,
          };


          for (
            const id
            of selectedArrowIds
          ) {

            const old =
              previous[id] ??
              {};


            next[id] = {
              ...old,
              enabled:
                old.enabled ??
                true,

              speed:
                old.speed ??
                DEFAULT_SPEED,

              direction:
                old.direction === -1
                  ? 1
                  : -1,
            };
          }


          return next;
        }
      );

    }, [selectedArrowIds]);


  /* -----------------------------------------------------
     Junction topology
  ----------------------------------------------------- */

  const updateJunctionPosition =
    useCallback((junctionId, x, y) => {
      setJunctions((previous) =>
        previous.map((junction) =>
          junction.id === junctionId
            ? { ...junction, x, y }
            : junction
        )
      );

      setElements((previous) => {
        const next = previous.map((element) => {
          const flow = flows[element.id];

          if (flow?.startJunctionId === junctionId) {
            return moveArrowEndpoint(
              element,
              "start",
              x,
              y
            );
          }

          if (flow?.endJunctionId === junctionId) {
            return moveArrowEndpoint(
              element,
              "end",
              x,
              y
            );
          }

          return element;
        });

        excalidrawApiRef.current?.updateScene({
          elements: next,
        });

        return next;
      });
    }, [flows]);


  const handleCanvasPointerDown =
    useCallback((event) => {
      if (
        !isPlacingJunction ||
        !appState ||
        event.target.closest?.(".flow-panel") ||
        event.target.closest?.(".flow-junction") ||
        event.target.closest?.(".flow-packet") ||
        event.target.closest?.(".packet-preview")
      ) {
        return;
      }

      event.preventDefault();
      event.stopPropagation();

      const { x, y } =
        viewportCoordsToSceneCoords(
          {
            clientX: event.clientX,
            clientY: event.clientY,
          },
          appState
        );

      const junction = {
        id: createJunctionId(),
        x,
        y,
        visible: true,
        size: 6,
        color: "#1b1b1f",
      };

      setJunctions((previous) => [
        ...previous,
        junction,
      ]);
      setSelectedJunctionId(junction.id);
      setIsPlacingJunction(false);
    }, [appState, isPlacingJunction]);


  const handleJunctionPointerDown =
    useCallback((event, junctionId) => {
      event.preventDefault();
      event.stopPropagation();
      setSelectedJunctionId(junctionId);

      const handleMove = (moveEvent) => {
        if (!appState) {
          return;
        }

        const { x, y } =
          viewportCoordsToSceneCoords(
            {
              clientX: moveEvent.clientX,
              clientY: moveEvent.clientY,
            },
            appState
          );

        updateJunctionPosition(
          junctionId,
          x,
          y
        );
      };

      const handleUp = () => {
        window.removeEventListener(
          "pointermove",
          handleMove
        );
        window.removeEventListener(
          "pointerup",
          handleUp
        );
      };

      window.addEventListener(
        "pointermove",
        handleMove
      );
      window.addEventListener(
        "pointerup",
        handleUp
      );
    }, [appState, updateJunctionPosition]);


  const handlePanelPointerDown =
    useCallback((event) => {
      if (event.button !== 0) {
        return;
      }

      event.preventDefault();
      const panel = panelRef.current;
      const rect = panel?.getBoundingClientRect();
      if (!rect) {
        return;
      }

      const offsetX = event.clientX - rect.left;
      const offsetY = event.clientY - rect.top;

      const handleMove = (moveEvent) => {
        const width = panelState.collapsed ? 56 : rect.width;
        const height = panelState.collapsed ? 44 : rect.height;
        setPanelState((previous) => ({
          ...previous,
          x: Math.min(
            Math.max(8, moveEvent.clientX - offsetX),
            Math.max(8, window.innerWidth - width - 8)
          ),
          y: Math.min(
            Math.max(8, moveEvent.clientY - offsetY),
            Math.max(8, window.innerHeight - height - 8)
          ),
        }));
      };

      const handleUp = () => {
        window.removeEventListener("pointermove", handleMove);
        window.removeEventListener("pointerup", handleUp);

        setPanelState((previous) => {
          const width = previous.collapsed ? 56 : rect.width;
          const distances = {
            left: previous.x,
            right: window.innerWidth - previous.x - width,
            top: previous.y,
            bottom: window.innerHeight - previous.y - rect.height,
          };
          const [edge, distance] = Object.entries(distances)
            .sort((a, b) => a[1] - b[1])[0];

          if (distance > 36) {
            return previous;
          }

          return {
            ...previous,
            x: edge === "left"
              ? 8
              : edge === "right"
                ? window.innerWidth - width - 8
                : previous.x,
            y: edge === "top"
              ? 8
              : edge === "bottom"
                ? Math.max(8, window.innerHeight - rect.height - 8)
                : previous.y,
          };
        });
      };

      window.addEventListener("pointermove", handleMove);
      window.addEventListener("pointerup", handleUp);
    }, [panelState.collapsed]);


  const bindSelectedArrows =
    useCallback((endpoint) => {
      const junction = junctions.find(
        ({ id }) => id === selectedJunctionId
      );

      if (!junction || !selectedArrowIds.length) {
        return;
      }

      setFlows((previous) => {
        const next = { ...previous };

        for (const arrowId of selectedArrowIds) {
          next[arrowId] = {
            enabled: previous[arrowId]?.enabled ?? true,
            speed: previous[arrowId]?.speed ?? DEFAULT_SPEED,
            direction: previous[arrowId]?.direction ?? 1,
            pattern: previous[arrowId]?.pattern ?? "dashes",
            startJunctionId:
              endpoint === "start"
                ? junction.id
                : previous[arrowId]?.startJunctionId,
            endJunctionId:
              endpoint === "end"
                ? junction.id
                : previous[arrowId]?.endJunctionId,
          };
        }

        return next;
      });

      setElements((previous) => {
        const next = previous.map((element) =>
          selectedArrowIds.includes(element.id)
            ? moveArrowEndpoint(
                element,
                endpoint,
                junction.x,
                junction.y
              )
            : element
        );

        excalidrawApiRef.current?.updateScene({
          elements: next,
        });

        return next;
      });
    }, [junctions, selectedArrowIds, selectedJunctionId]);


  const deleteSelectedJunction =
    useCallback(() => {
      if (!selectedJunctionId) {
        return;
      }

      setJunctions((previous) =>
        previous.filter(
          ({ id }) => id !== selectedJunctionId
        )
      );

      setFlows((previous) =>
        Object.fromEntries(
          Object.entries(previous).map(([id, flow]) => [
            id,
            {
              ...flow,
              startJunctionId:
                flow.startJunctionId === selectedJunctionId
                  ? undefined
                  : flow.startJunctionId,
              endJunctionId:
                flow.endJunctionId === selectedJunctionId
                  ? undefined
                  : flow.endJunctionId,
            },
          ])
        )
      );

      setSelectedJunctionId(null);
    }, [selectedJunctionId]);


  const detachSelectedArrows =
    useCallback((endpoint) => {
      if (!selectedArrowIds.length) {
        return;
      }

      setFlows((previous) => {
        const next = { ...previous };

        for (const arrowId of selectedArrowIds) {
          if (!previous[arrowId]) {
            continue;
          }

          next[arrowId] = { ...previous[arrowId] };
          delete next[arrowId][`${endpoint}JunctionId`];
        }

        return next;
      });
    }, [selectedArrowIds]);


  const updateSelectedJunction =
    useCallback((updates) => {
      if (!selectedJunctionId) {
        return;
      }

      setJunctions((previous) =>
        previous.map((junction) =>
          junction.id === selectedJunctionId
            ? { ...junction, ...updates }
            : junction
        )
      );
    }, [selectedJunctionId]);


  const selectedPacket = packets.find(
    (packet) => packet.id === selectedPacketId
  ) ?? null;


  const selectPacketFromCanvas = useCallback((packetId, pointer) => {
    setSelectedPacketId(packetId);
    setPacketPreview({
      packetId,
      x: pointer
        ? pointer.clientX - containerRect.left
        : containerRect.width / 2,
      y: pointer
        ? pointer.clientY - containerRect.top
        : containerRect.height / 2,
    });
  }, [containerRect]);


  const addPacket = useCallback(() => {
    if (selectedArrowIds.length !== 1) {
      return;
    }

    const packet = {
      id: createJunctionId(),
      arrowId: selectedArrowIds[0],
      label: `Packet ${packets.length + 1}`,
      type: "data",
      status: "moving",
      color: "#4c6ef5",
      payload: "{}",
    };

    setPackets((previous) => [...previous, packet]);
    setSelectedPacketId(packet.id);
    setFlows((previous) => ({
      ...previous,
      [selectedArrowIds[0]]: {
        ...previous[selectedArrowIds[0]],
        enabled: true,
        speed: previous[selectedArrowIds[0]]?.speed ?? DEFAULT_SPEED,
        direction: previous[selectedArrowIds[0]]?.direction ?? 1,
        pattern: "lumps",
      },
    }));
  }, [packets.length, selectedArrowIds]);


  const updateSelectedPacket = useCallback((updates) => {
    if (!selectedPacketId) {
      return;
    }

    setPackets((previous) =>
      previous.map((packet) =>
        packet.id === selectedPacketId
          ? { ...packet, ...updates }
          : packet
      )
    );
  }, [selectedPacketId]);


  const deleteSelectedPacket = useCallback(() => {
    if (!selectedPacketId) {
      return;
    }

    setPackets((previous) =>
      previous.filter((packet) =>
        packet.id !== selectedPacketId
      )
    );
    setSelectedPacketId(null);
    setPacketPreview(null);
  }, [selectedPacketId]);


  /* -----------------------------------------------------
     Clear everything
  ----------------------------------------------------- */

  const clearStorage = () => {

    localStorage.removeItem(
      SCENE_STORAGE_KEY
    );

    localStorage.removeItem(
      FLOW_STORAGE_KEY
    );

    localStorage.removeItem(
      JUNCTION_STORAGE_KEY
    );

    localStorage.removeItem(PACKET_STORAGE_KEY);

    clearTimeout(saveTimerRef.current);
    setElements([]);
    setFlows({});
    setJunctions([]);
    setPackets([]);
    setSelectedPacketId(null);
    setPacketPreview(null);
    setSelectedJunctionId(null);
    setGlobalPaused(false);
    setAppState((previous) => previous
      ? { ...previous, selectedElementIds: {} }
      : previous
    );
    excalidrawApiRef.current?.resetScene();
  };


  /* =====================================================
     UI
  ===================================================== */

  return (
    <>


      <div
        ref={wrapperRef}

        className="flowdraw-app"

        onPointerDownCapture={
          handleCanvasPointerDown
        }
      >

        <Excalidraw
          excalidrawAPI={setExcalidrawApi}

          initialData={
            initialData
          }

          onChange={
            handleChange
          }
        />


        <FlowOverlay
          elements={
            elements
          }

          flows={
            flows
          }

          junctions={
            junctions
          }

          packets={packets}

          globalPaused={globalPaused}

          appState={
            appState
          }

          containerRect={
            containerRect
          }

          selectedJunctionId={
            selectedJunctionId
          }

          onJunctionPointerDown={
            handleJunctionPointerDown
          }

          selectedPacketId={selectedPacketId}

          onPacketSelect={selectPacketFromCanvas}
        />


        {packetPreview && (() => {
          const packet = packets.find(
            ({ id }) => id === packetPreview.packetId
          );

          if (!packet) {
            return null;
          }

          return (
            <aside
              className="packet-preview"
              style={{
                left: Math.min(
                  Math.max(12, packetPreview.x + 14),
                  Math.max(12, containerRect.width - 312)
                ),
                top: Math.min(
                  Math.max(12, packetPreview.y + 14),
                  Math.max(12, containerRect.height - 250)
                ),
              }}
              aria-label={`Payload preview for ${packet.label}`}
            >
              <div className="packet-preview-header">
                <strong>{packet.label || "Packet"}</strong>
                <button
                  type="button"
                  aria-label="Close payload preview"
                  onClick={() => setPacketPreview(null)}
                >
                  ×
                </button>
              </div>

              <div className="packet-preview-meta">
                <span>{packet.type || "data"}</span>
                <span>{packet.status || "moving"}</span>
              </div>

              <pre>{formatPacketPayload(packet.payload)}</pre>

              <button
                type="button"
                onClick={() => {
                  setSelectedPacketId(packet.id);
                  setPanelState((previous) => ({
                    ...previous,
                    collapsed: false,
                  }));
                  setPacketPreview(null);
                }}
              >
                Edit packet
              </button>
            </aside>
          );
        })()}


        <div
          ref={panelRef}
          className={
            `flow-panel${panelState.collapsed
              ? " flow-panel--collapsed"
              : ""}`
          }
          style={{
            left: panelState.x,
            top: panelState.y,
          }}
        >

          <div
            className="flow-panel-header"
            onPointerDown={handlePanelPointerDown}
          >
            <span className="flow-panel-grip">⠿</span>
            <span className="flow-title">Flow</span>
            <button
              type="button"
              className="flow-panel-collapse"
              aria-label={
                panelState.collapsed
                  ? "Expand flow tools"
                  : "Minimize flow tools"
              }
              title={
                panelState.collapsed
                  ? "Expand"
                  : "Minimize"
              }
              onPointerDown={(event) => event.stopPropagation()}
              onClick={() =>
                setPanelState((previous) => ({
                  ...previous,
                  collapsed: !previous.collapsed,
                }))
              }
            >
              {panelState.collapsed ? "+" : "−"}
            </button>
          </div>


          {!panelState.collapsed && (
            <div className="flow-panel-content">


          <div className="flow-title">
            Flow Arrow
          </div>


          <div
            className="flow-status"
          >
            {hasSelectedArrow
              ? `${selectedArrowIds.length} arrow(s) selected`
              : "Select an arrow"}
          </div>


          <button
            disabled={
              !hasSelectedArrow
            }

            className={
              allSelectedAreOn
                ? "flow-active"
                : ""
            }

            onClick={
              toggleFlow
            }
          >
            {allSelectedAreOn
              ? "Flow OFF"
              : "Flow ON"}
          </button>


          <FlowPatternSelect
            value={displayedPattern}
            disabled={!hasSelectedArrow}
            onChange={setPattern}
          />


          <button
            disabled={
              !hasSelectedArrow
            }

            onClick={
              reverseDirection
            }
          >
            Reverse direction
          </button>


          <div className="flow-pause-row">
            <button
              disabled={!hasSelectedArrow}
              className={
                allSelectedArePaused
                  ? "flow-active"
                  : ""
              }
              onClick={toggleSelectedPause}
            >
              {allSelectedArePaused
                ? "Resume selected"
                : "Pause selected"}
            </button>

            <button
              className={globalPaused ? "flow-active" : ""}
              onClick={() =>
                setGlobalPaused((paused) => !paused)
              }
            >
              {globalPaused ? "Resume all" : "Pause all"}
            </button>
          </div>


          <div
            className="speed-row"
          >
            <span>
              Speed
            </span>

            <span>
              {displayedSpeed}
            </span>
          </div>


          <input
            className=
              "speed-slider"

            type="range"

            min="30"
            max="400"
            step="10"

            value={
              displayedSpeed
            }

            disabled={
              !hasSelectedArrow
            }

            onChange={
              (event) => {

                setSpeed(
                  Number(
                    event
                      .target
                      .value
                  )
                );
              }
            }
          />


          <div
            className="divider"
          />


          <div className="flow-title">
            Data packets
          </div>


          <button
            disabled={selectedArrowIds.length !== 1}
            onClick={addPacket}
          >
            Add packet to arrow
          </button>


          <select
            aria-label="Selected data packet"
            value={selectedPacketId ?? ""}
            onChange={(event) =>
              setSelectedPacketId(event.target.value || null)
            }
          >
            <option value="">Select packet</option>
            {packets.map((packet) => (
              <option key={packet.id} value={packet.id}>
                {packet.label || "Packet"}
              </option>
            ))}
          </select>


          {selectedPacket && (
            <div className="packet-editor">
              <label>
                <span>Label</span>
                <input
                  value={selectedPacket.label}
                  onChange={(event) =>
                    updateSelectedPacket({ label: event.target.value })
                  }
                />
              </label>

              <label>
                <span>Type</span>
                <input
                  value={selectedPacket.type}
                  onChange={(event) =>
                    updateSelectedPacket({ type: event.target.value })
                  }
                />
              </label>

              <label>
                <span>Status</span>
                <select
                  value={selectedPacket.status}
                  onChange={(event) =>
                    updateSelectedPacket({ status: event.target.value })
                  }
                >
                  <option value="queued">Queued</option>
                  <option value="moving">Moving</option>
                  <option value="delivered">Delivered</option>
                  <option value="failed">Failed</option>
                </select>
              </label>

              <label className="speed-row">
                <span>Color</span>
                <input
                  type="color"
                  value={selectedPacket.color}
                  onChange={(event) =>
                    updateSelectedPacket({ color: event.target.value })
                  }
                />
              </label>

              <label>
                <span>Payload</span>
                <textarea
                  rows="5"
                  spellCheck="false"
                  value={selectedPacket.payload}
                  placeholder={'{"key": "value"}'}
                  onChange={(event) =>
                    updateSelectedPacket({ payload: event.target.value })
                  }
                />
              </label>

              <div className="flow-status">
                Arrow: {selectedPacket.arrowId.slice(0, 8)}…
              </div>

              <button onClick={deleteSelectedPacket}>
                Delete packet
              </button>
            </div>
          )}


          <div
            className="divider"
          />


          <div
            className="flow-title"
          >
            Junction
          </div>


          <button
            className={
              isPlacingJunction
                ? "flow-active"
                : ""
            }

            onClick={() =>
              setIsPlacingJunction(
                (active) => !active
              )
            }
          >
            {isPlacingJunction
              ? "Click canvas to place"
              : "Place junction"}
          </button>


          <div className="flow-status">
            {selectedJunctionTopology
              ? `${selectedJunctionTopology.kind}: ${selectedJunctionTopology.incoming} in / ${selectedJunctionTopology.outgoing} out`
              : "Select a junction"}
          </div>


          <select
            aria-label="Selected junction"
            value={selectedJunctionId ?? ""}
            onChange={(event) =>
              setSelectedJunctionId(
                event.target.value || null
              )
            }
          >
            <option value="">Select junction</option>
            {junctions.map((junction, index) => (
              <option key={junction.id} value={junction.id}>
                Junction {index + 1}
                {junction.visible ? "" : " (hidden)"}
              </option>
            ))}
          </select>


          <button
            disabled={
              !selectedJunctionId ||
              !hasSelectedArrow
            }

            onClick={() =>
              bindSelectedArrows("start")
            }
          >
            Bind arrow start
          </button>


          <button
            disabled={
              !selectedJunctionId ||
              !hasSelectedArrow
            }

            onClick={() =>
              bindSelectedArrows("end")
            }
          >
            Bind arrow end
          </button>


          <button
            disabled={!hasSelectedArrow}
            onClick={() => detachSelectedArrows("start")}
          >
            Detach arrow start
          </button>


          <button
            disabled={!hasSelectedArrow}
            onClick={() => detachSelectedArrows("end")}
          >
            Detach arrow end
          </button>


          <label className="speed-row">
            <span>Visible</span>
            <input
              type="checkbox"
              disabled={!selectedJunctionId}
              checked={
                junctions.find(
                  ({ id }) => id === selectedJunctionId
                )?.visible ?? false
              }
              onChange={(event) =>
                updateSelectedJunction({
                  visible: event.target.checked,
                })
              }
            />
          </label>


          <label className="speed-row">
            <span>Size</span>
            <input
              type="range"
              min="3"
              max="14"
              disabled={!selectedJunctionId}
              value={
                junctions.find(
                  ({ id }) => id === selectedJunctionId
                )?.size ?? 6
              }
              onChange={(event) =>
                updateSelectedJunction({
                  size: Number(event.target.value),
                })
              }
            />
          </label>


          <label className="speed-row">
            <span>Color</span>
            <input
              type="color"
              disabled={!selectedJunctionId}
              value={
                junctions.find(
                  ({ id }) => id === selectedJunctionId
                )?.color ?? "#1b1b1f"
              }
              onChange={(event) =>
                updateSelectedJunction({
                  color: event.target.value,
                })
              }
            />
          </label>


          <button
            disabled={!selectedJunctionId}
            onClick={deleteSelectedJunction}
          >
            Delete junction
          </button>


          <div
            className="divider"
          />


          <button
            onClick={
              clearStorage
            }
          >
            Clear saved data
          </button>

            </div>
          )}

        </div>

      </div>
    </>
  );
}


createRoot(
  document.getElementById(
    "root"
  )
).render(
  <App />
);
