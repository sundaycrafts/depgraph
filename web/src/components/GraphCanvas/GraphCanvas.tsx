import {
    MarkerType,
    ReactFlow,
    ReactFlowProvider,
    useEdgesState,
    useNodesState,
    useReactFlow,
    type Edge as RFEdge,
} from "@xyflow/react";
import { useEffect, useMemo, useState } from "react";
import "@xyflow/react/dist/style.css";

import type { Graph, Node as DomainNode } from "../../schemas/api";
import { selectVisibleNodes } from "../../lib/visibleNodes";
import {
    FolderGroupNode,
    type FolderGroupNodeType,
} from "./FolderGroupNode";
import { EntityNode, type EntityNodeType } from "./EntityNode";

interface Props {
    graph: Graph;
    onNodeSelect: (node: DomainNode) => void;
    selectedKinds: string[];
    limitNodes?: boolean;
    searchQuery?: string;
    showEdges?: boolean;
}

export type GraphRFNode = FolderGroupNodeType | EntityNodeType;
type GraphRFEdge = RFEdge;

const SYMBOL_BASE_HSL = { h: 53, s: 96, l: 88 } as const; // #fef9c3
const SYMBOL_HOT_HSL = { h: 0, s: 85, l: 82 } as const; // max-references red
const HIGHLIGHT_BG = "#93c5fd"; // blue-300 — selection
const SEARCH_MATCH_BG = "#bae6fd"; // sky-200 — search match (lighter, distinct from selection)
const FOLDER_MIN_COLS = 3;
const FOLDER_MAX_COLS = 6;
const INITIAL_ZOOM = 0.5;
const MIN_ZOOM = 0.2;
const NODE_W = 150;
const NODE_H = 48;
const GAP = Math.max(4, Math.round(NODE_H * 0.1));
const COL_WIDTH = NODE_W + GAP;
const ROW_HEIGHT = NODE_H + GAP;
// Folder-group container layout.
const FOLDER_HEADER_H = 18;
const FOLDER_PADDING = 6;
const FOLDER_GAP = GAP * 2;
// Soft cap on a row's total folder width before wrapping. Sized so the
// canvas can pack a handful of typical-width folders side by side.
const ROW_WIDTH_BUDGET = FOLDER_MAX_COLS * 4 * COL_WIDTH;

// symbolBg picks a symbol node background based on its incoming reference
// count. Zero → white (no dependants). Otherwise the gradient runs from
// base (yellow) at `lo` to hot (red) at `hi`, with values outside [lo, hi]
// clamped to the endpoints — so a single huge outlier doesn't compress the
// whole gradient into the dim end.
function symbolBg(refCount: number, lo: number, hi: number): string {
    if (refCount === 0) {
        return "#ffffff";
    }
    const denom = hi - lo;
    const t = denom <= 0 ? 0 : Math.min(Math.max((refCount - lo) / denom, 0), 1);
    const h = SYMBOL_BASE_HSL.h + (SYMBOL_HOT_HSL.h - SYMBOL_BASE_HSL.h) * t;
    const s = SYMBOL_BASE_HSL.s + (SYMBOL_HOT_HSL.s - SYMBOL_BASE_HSL.s) * t;
    const l = SYMBOL_BASE_HSL.l + (SYMBOL_HOT_HSL.l - SYMBOL_BASE_HSL.l) * t;
    return `hsl(${h.toFixed(1)}, ${s.toFixed(1)}%, ${l.toFixed(1)}%)`;
}

// fuzzyMatch returns true if every char of query appears in target in order
// (case-insensitive subsequence match — the "Cmd-T" style fuzzy finder).
function fuzzyMatch(query: string, target: string): boolean {
    if (!query) return false;
    const q = query.toLowerCase();
    const t = target.toLowerCase();
    let qi = 0;
    for (let ti = 0; ti < t.length && qi < q.length; ti++) {
        if (t[ti] === q[qi]) qi++;
    }
    return qi === q.length;
}

// gatherDependants walks edges upstream from rootId (e.to === cur → add e.from)
// and returns the transitive set of ancestor node IDs, excluding rootId itself.
function gatherDependants(
    rootId: string,
    edges: { from: string; to: string }[],
): Set<string> {
    const result = new Set<string>();
    const queue = [rootId];
    while (queue.length > 0) {
        const cur = queue.shift()!;
        for (const e of edges) {
            if (e.to === cur && e.from !== rootId && !result.has(e.from)) {
                result.add(e.from);
                queue.push(e.from);
            }
        }
    }
    return result;
}

function dirname(path: string): string {
    const i = path.lastIndexOf("/");
    return i >= 0 ? path.slice(0, i) : "";
}

function basename(path: string): string {
    const i = path.lastIndexOf("/");
    return i >= 0 ? path.slice(i + 1) : path;
}

// commonFolderPrefix takes the longest folder path and trims one trailing
// path segment at a time until every folder starts with the result. The
// returned prefix always ends with "/" (or is empty when nothing's shared).
function commonFolderPrefix(folders: string[]): string {
    if (folders.length === 0) return "";
    let candidate = folders[0];
    for (const f of folders) if (f.length > candidate.length) candidate = f;
    while (candidate.length > 0) {
        if (folders.every((f) => f === candidate || f.startsWith(candidate + "/"))) {
            return candidate + "/";
        }
        const lastSlash = candidate.lastIndexOf("/");
        if (lastSlash < 0) break;
        candidate = candidate.slice(0, lastSlash);
    }
    return "";
}

function GraphCanvasInner({
    graph,
    onNodeSelect,
    selectedKinds,
    limitNodes = false,
    searchQuery = "",
    showEdges = false,
}: Props) {
    const { fitView, getInternalNode } = useReactFlow<
        GraphRFNode,
        GraphRFEdge
    >();
    const [selectedNodeId, setSelectedNodeId] = useState<string | null>(null);

    const nodeTypes = useMemo(
        () => ({ folderGroup: FolderGroupNode, entityNode: EntityNode }),
        [],
    );

    const visibleDomainNodes = useMemo(
        () => selectVisibleNodes(graph, selectedKinds, limitNodes),
        [graph, selectedKinds, limitNodes],
    );

    const visibleDomainEdges = useMemo(() => {
        const visibleIds = new Set(visibleDomainNodes.map((n) => n.id));
        return graph.edges.filter(
            (e) => visibleIds.has(e.from) && visibleIds.has(e.to),
        );
    }, [graph.edges, visibleDomainNodes]);

    // BFS upstream: find selectedNodeId and all nodes that (transitively) depend on it.
    const highlightedIds = useMemo<Set<string>>(() => {
        if (!selectedNodeId) return new Set();
        const result = new Set<string>([selectedNodeId]);
        const queue = [selectedNodeId];
        while (queue.length > 0) {
            const cur = queue.shift()!;
            for (const e of visibleDomainEdges) {
                if (e.to === cur && !result.has(e.from)) {
                    result.add(e.from);
                    queue.push(e.from);
                }
            }
        }
        return result;
    }, [selectedNodeId, visibleDomainEdges]);

    const searchMatchedIds = useMemo<Set<string>>(() => {
        const result = new Set<string>();
        if (!searchQuery) return result;
        for (const n of visibleDomainNodes) {
            if (fuzzyMatch(searchQuery, n.label)) result.add(n.id);
        }
        return result;
    }, [searchQuery, visibleDomainNodes]);

    // Incoming "references" count per node. Computed from the full graph so
    // it stays stable as the user toggles filter chips.
    const refCountByNodeId = useMemo(() => {
        const m = new Map<string, number>();
        for (const e of graph.edges) {
            if (e.kind !== "references") continue;
            m.set(e.to, (m.get(e.to) ?? 0) + 1);
        }
        return m;
    }, [graph.edges]);

    // For each symbol, the dependee it should sit next to: the visible-graph
    // outgoing-references target with the highest refCount. Falls back to the
    // node itself when it depends on nothing visible. Used as the primary sort
    // key so dependants land just before their hottest dependee.
    const anchorByNodeId = useMemo(() => {
        const m = new Map<string, string>();
        const visibleIds = new Set(visibleDomainNodes.map((n) => n.id));
        const outTargets = new Map<string, string[]>();
        for (const e of visibleDomainEdges) {
            if (e.kind !== "references") continue;
            const arr = outTargets.get(e.from) ?? [];
            arr.push(e.to);
            outTargets.set(e.from, arr);
        }
        for (const n of visibleDomainNodes) {
            if (n.kind !== "symbol") continue;
            const targets = outTargets.get(n.id);
            if (!targets || targets.length === 0) {
                m.set(n.id, n.id);
                continue;
            }
            let best = n.id;
            let bestRef = -1;
            for (const t of targets) {
                if (!visibleIds.has(t)) continue;
                const r = refCountByNodeId.get(t) ?? 0;
                if (r > bestRef) {
                    best = t;
                    bestRef = r;
                }
            }
            m.set(n.id, best);
        }
        return m;
    }, [visibleDomainNodes, visibleDomainEdges, refCountByNodeId]);

    const flowNodes = useMemo<GraphRFNode[]>(() => {
        // File nodes are subsumed by the folder grouping; only render symbols.
        const symbols = visibleDomainNodes.filter((n) => n.kind === "symbol");

        // Group symbols by their containing folder (path minus filename, no
        // nesting — siblings only).
        const byFolder = new Map<string, DomainNode[]>();
        for (const n of symbols) {
            const folder = dirname(n.path ?? "");
            const arr = byFolder.get(folder) ?? [];
            arr.push(n);
            byFolder.set(folder, arr);
        }

        // Compute the longest path prefix shared by every folder so we can
        // shorten display labels to "@/<rest>". Stable across filter changes
        // would require running this on all nodes, but per-frame on visible
        // is fine — folders rarely change drastically across filter toggles.
        const sharedPrefix = commonFolderPrefix(Array.from(byFolder.keys()));

        // Two-stage sort applied per folder:
        //   Stage 1 — local clustering by anchor (dependee with the highest
        //   refCount), so dependants of the same anchor land adjacent.
        //   Stage 2 — stable sort by own refCount ASC, pushing hot symbols
        //   to the bottom of the folder while preserving stage-1 adjacency
        //   within a tier.
        const sortMembers = (members: DomainNode[]): DomainNode[] => {
            const stage1 = members.slice().sort((a, b) => {
                const aAnchor = anchorByNodeId.get(a.id) ?? a.id;
                const bAnchor = anchorByNodeId.get(b.id) ?? b.id;
                const aGroup = refCountByNodeId.get(aAnchor) ?? 0;
                const bGroup = refCountByNodeId.get(bAnchor) ?? 0;
                if (aGroup !== bGroup) return aGroup - bGroup;
                if (aAnchor !== bAnchor)
                    return aAnchor < bAnchor ? -1 : 1;
                const aIsAnchor = aAnchor === a.id ? 1 : 0;
                const bIsAnchor = bAnchor === b.id ? 1 : 0;
                return aIsAnchor - bIsAnchor;
            });
            const stage1Index = new Map<string, number>();
            stage1.forEach((n, i) => stage1Index.set(n.id, i));
            return stage1.slice().sort((a, b) => {
                const aR = refCountByNodeId.get(a.id) ?? 0;
                const bR = refCountByNodeId.get(b.id) ?? 0;
                if (aR !== bR) return aR - bR;
                return (
                    (stage1Index.get(a.id) ?? 0) -
                    (stage1Index.get(b.id) ?? 0)
                );
            });
        };

        // Order folders lexicographically by path: a parent folder's path is
        // a prefix of its descendants', so this naturally yields parent →
        // nested traversal. Top-level ordering ends up alphabetical, which
        // the user has said is fine.
        const orderedFolders = Array.from(byFolder.entries())
            .map(([folder, members]) => ({
                folder,
                members: sortMembers(members),
            }))
            .sort((a, b) => a.folder.localeCompare(b.folder));

        // Color gradient endpoints from the IQR (Q1–Q3) of refCount so a
        // single huge outlier doesn't compress everything else into the dim
        // end. Computed across all visible symbols so a hot node looks the
        // same regardless of its folder neighborhood. Zero refCount nodes
        // are excluded — they always render white.
        const positiveRefs = symbols
            .map((n) => refCountByNodeId.get(n.id) ?? 0)
            .filter((c) => c > 0)
            .sort((a, b) => a - b);
        const q = (frac: number) =>
            positiveRefs.length === 0
                ? 0
                : positiveRefs[
                      Math.min(
                          positiveRefs.length - 1,
                          Math.floor(positiveRefs.length * frac),
                      )
                  ];
        const refLo = q(0.25);
        const refHi = q(0.75);

        // Shelf-pack folder boxes left→right, wrapping when the row budget
        // is full. Row height = max folder height in that row.
        const placed: Array<{
            folder: string;
            members: DomainNode[];
            cols: number;
            width: number;
            height: number;
            x: number;
            y: number;
        }> = [];
        let cursorX = 0;
        let cursorY = 0;
        let rowMaxH = 0;
        for (const { folder, members } of orderedFolders) {
            const cols = Math.min(
                FOLDER_MAX_COLS,
                Math.max(FOLDER_MIN_COLS, members.length),
            );
            const rows = Math.ceil(members.length / cols);
            const width = cols * COL_WIDTH + 2 * FOLDER_PADDING;
            const height =
                FOLDER_HEADER_H + rows * ROW_HEIGHT + 2 * FOLDER_PADDING;
            if (cursorX > 0 && cursorX + width > ROW_WIDTH_BUDGET) {
                cursorY += rowMaxH + FOLDER_GAP;
                cursorX = 0;
                rowMaxH = 0;
            }
            placed.push({
                folder,
                members,
                cols,
                width,
                height,
                x: cursorX,
                y: cursorY,
            });
            cursorX += width + FOLDER_GAP;
            if (height > rowMaxH) rowMaxH = height;
        }

        // Emit one folderGroup node + N entityNode children per folder.
        // Parent must precede children in the array for React Flow.
        const result: GraphRFNode[] = [];
        for (const p of placed) {
            const groupId = `folder:${p.folder}`;
            const displayFolder = p.folder.startsWith(sharedPrefix)
                ? `@/${p.folder.slice(sharedPrefix.length)}`
                : p.folder;
            result.push({
                id: groupId,
                type: "folderGroup",
                position: { x: p.x, y: p.y },
                style: { width: p.width, height: p.height },
                data: { folder: displayFolder },
                selectable: false,
                // Draggable: dragging the group moves all child nodes with it
                // (React Flow handles children-follow-parent natively).
                draggable: true,
            });
            p.members.forEach((n, i) => {
                const col = i % p.cols;
                const row = Math.floor(i / p.cols);
                const refCount = refCountByNodeId.get(n.id) ?? 0;
                const isHighlighted = highlightedIds.has(n.id);
                const isSearchMatch = searchMatchedIds.has(n.id);
                const bg = isHighlighted
                    ? HIGHLIGHT_BG
                    : isSearchMatch
                      ? SEARCH_MATCH_BG
                      : symbolBg(refCount, refLo, refHi);
                const filename = basename(n.path ?? "");
                result.push({
                    id: n.id,
                    type: "entityNode",
                    parentId: groupId,
                    position: {
                        x: FOLDER_PADDING + col * COL_WIDTH,
                        y:
                            FOLDER_PADDING +
                            FOLDER_HEADER_H +
                            row * ROW_HEIGHT,
                    },
                    style: { width: NODE_W, height: NODE_H },
                    data: {
                        label: n.label,
                        header: `(${refCount})${filename}`,
                        bg,
                        domainNode: n,
                    },
                });
            });
        }
        return result;
    }, [
        visibleDomainNodes,
        refCountByNodeId,
        anchorByNodeId,
        highlightedIds,
        searchMatchedIds,
    ]);

    const flowEdges = useMemo<GraphRFEdge[]>(() => {
        if (!showEdges) return [];
        return visibleDomainEdges.map((e) => {
            const highlighted =
                highlightedIds.has(e.from) && highlightedIds.has(e.to);
            const color = highlighted
                ? "#2563eb" // blue-600
                : e.kind === "defines"
                  ? "#e2e8f0" // slate-200
                  : "#f8fafc"; // slate-50 — lightest available
            return {
                id: e.id,
                source: e.from,
                target: e.to,
                selectable: false,
                focusable: false,
                interactionWidth: 0,
                // Push edges below child nodes (which auto-elevate due to
                // parent grouping) so the line passes behind folder borders
                // and labels.
                zIndex: -1,
                style: { stroke: color },
                markerEnd: {
                    type: MarkerType.ArrowClosed,
                    color,
                    width: 32,
                    height: 32,
                },
            };
        });
    }, [visibleDomainEdges, highlightedIds, showEdges]);

    const [nodes, setNodes, onNodesChange] =
        useNodesState<GraphRFNode>(flowNodes);
    const [edges, setEdges, onEdgesChange] =
        useEdgesState<GraphRFEdge>(flowEdges);

    useEffect(() => {
        setNodes((current) => {
            const posMap = new Map(current.map((n) => [n.id, n.position]));
            return flowNodes.map((n) => ({
                ...n,
                position: posMap.get(n.id) ?? n.position,
            }));
        });
    }, [flowNodes, setNodes]);

    useEffect(() => {
        setEdges(flowEdges);
    }, [flowEdges, setEdges]);

    useEffect(() => {
        window.requestAnimationFrame(() => {
            fitView({
                padding: 0.2,
                minZoom: INITIAL_ZOOM,
                maxZoom: INITIAL_ZOOM,
            });
        });
    }, [nodes.length, edges.length, fitView]);

    return (
        <div style={{ width: "100%", height: "100%" }}>
            <ReactFlow
                nodes={nodes}
                edges={edges}
                nodeTypes={nodeTypes}
                minZoom={MIN_ZOOM}
                onNodesChange={onNodesChange}
                onEdgesChange={onEdgesChange}
                onNodeClick={(event, node) => {
                    if (node.type !== "entityNode") return;
                    if (event.altKey) {
                        // Alt+click: gather dependants and stack them above
                        // the clicked node in a 5-wide grid (anchor stays).
                        // Use absolute coordinates and strip parentId so the
                        // moved nodes escape their folder boxes consistently.
                        const deps = gatherDependants(
                            node.id,
                            visibleDomainEdges,
                        );
                        const internal = getInternalNode(node.id);
                        const Cx = internal?.internals.positionAbsolute.x ?? 0;
                        const Cy = internal?.internals.positionAbsolute.y ?? 0;
                        setNodes((nds) => {
                            const orderedDeps = nds.filter((n) =>
                                deps.has(n.id),
                            );
                            const newPos = new Map<
                                string,
                                { x: number; y: number }
                            >();
                            orderedDeps.forEach((d, i) => {
                                const col = i % 5;
                                const rowFromBottom = Math.floor(i / 5);
                                newPos.set(d.id, {
                                    x: Cx + (col - 2) * COL_WIDTH,
                                    y: Cy - (rowFromBottom + 1) * ROW_HEIGHT,
                                });
                            });
                            return nds.map((n) => {
                                const p = newPos.get(n.id);
                                if (!p) return n;
                                return {
                                    ...n,
                                    parentId: undefined,
                                    position: p,
                                };
                            });
                        });
                    }
                    setSelectedNodeId((prev) =>
                        prev === node.id ? null : node.id,
                    );
                    onNodeSelect(node.data.domainNode);
                }}
                onPaneClick={() => setSelectedNodeId(null)}
                nodesDraggable
            />
        </div>
    );
}

export function GraphCanvas(props: Props) {
    return (
        <ReactFlowProvider>
            <GraphCanvasInner {...props} />
        </ReactFlowProvider>
    );
}
