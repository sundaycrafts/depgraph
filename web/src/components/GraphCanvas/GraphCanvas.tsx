import {
    MarkerType,
    ReactFlow,
    ReactFlowProvider,
    useEdgesState,
    useNodesState,
    useReactFlow,
    type Edge as RFEdge,
    type Node as RFNode,
} from "@xyflow/react";
import { useEffect, useMemo, useState } from "react";
import "@xyflow/react/dist/style.css";

import type { Graph, Node as DomainNode } from "../../schemas/api";
import { selectVisibleNodes } from "../../lib/visibleNodes";

interface Props {
    graph: Graph;
    onNodeSelect: (node: DomainNode) => void;
    selectedKinds: string[];
    limitNodes?: boolean;
    searchQuery?: string;
}

type GraphNodeData = {
    label: string;
    domainNode: DomainNode;
};

export type GraphRFNode = RFNode<GraphNodeData>;
type GraphRFEdge = RFEdge;

const FILE_BG = "#dbeafe";
const SYMBOL_BASE_HSL = { h: 53, s: 96, l: 88 } as const; // #fef9c3
const SYMBOL_HOT_HSL = { h: 0, s: 85, l: 82 } as const; // max-references red
const HIGHLIGHT_BG = "#93c5fd"; // blue-300 — more vivid than file-node blue (#dbeafe)
const SEARCH_MATCH_BG = "#bae6fd"; // sky-200 — a lighter blue, distinct from selection HIGHLIGHT_BG
const COLS = 8;
const INITIAL_ZOOM = 0.5;
const MIN_ZOOM = 0.2;
// Layout grid: NODE_W/NODE_H are the assumed node body sizes; GAP is the
// inter-node whitespace, fixed at 10% of NODE_H. COL_WIDTH / ROW_HEIGHT /
// FILE_SYMBOL_GAP all derive from the same GAP so spacing stays consistent.
const NODE_W = 150;
const NODE_H = 48;
const GAP = Math.max(4, Math.round(NODE_H * 0.1));
const COL_WIDTH = NODE_W + GAP;
const ROW_HEIGHT = NODE_H + GAP;
const FILE_SYMBOL_GAP = GAP;

// symbolBg picks a symbol node background based on its incoming reference
// count: zero → white (no dependants), one → base (yellow), interpolating
// toward hot (red) as the count climbs to maxRefCount.
function symbolBg(refCount: number, maxRefCount: number): string {
    if (refCount === 0) {
        return "#ffffff";
    }
    const denom = Math.max(maxRefCount - 1, 1);
    const t = Math.min((refCount - 1) / denom, 1);
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

function GraphCanvasInner({
    graph,
    onNodeSelect,
    selectedKinds,
    limitNodes = false,
    searchQuery = "",
}: Props) {
    const { fitView } = useReactFlow<GraphRFNode, GraphRFEdge>();
    const [selectedNodeId, setSelectedNodeId] = useState<string | null>(null);

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
        const baseStyle = {
            border: "1px solid #94a3b8",
            borderRadius: "6px",
            fontSize: "12px",
            padding: "4px 8px",
            width: NODE_W,
            height: NODE_H,
            whiteSpace: "normal" as const,
            overflowWrap: "anywhere" as const,
            lineHeight: 1.2,
            overflow: "hidden" as const,
        };
        const highlightStyle = (id: string) =>
            highlightedIds.has(id) ? { background: HIGHLIGHT_BG } : {};
        const searchMatchStyle = (id: string) =>
            searchMatchedIds.has(id) ? { background: SEARCH_MATCH_BG } : {};

        const files = visibleDomainNodes.filter((n) => n.kind === "file");
        // Two-stage symbol order:
        //   Stage 1 — local clustering: place each symbol next to the dependee
        //   it points at most strongly (anchorByNodeId) so dependants of the
        //   same anchor land adjacent.
        //   Stage 2 — global push-down: stable-sort the whole sequence by own
        //   refCount ascending. Hot nodes (high refCount) end up at the very
        //   bottom; within a same-refCount tier the stage-1 clustering is
        //   preserved by sort stability.
        const stage1 = visibleDomainNodes
            .filter((n) => n.kind === "symbol")
            .slice()
            .sort((a, b) => {
                const aAnchor = anchorByNodeId.get(a.id) ?? a.id;
                const bAnchor = anchorByNodeId.get(b.id) ?? b.id;
                const aGroup = refCountByNodeId.get(aAnchor) ?? 0;
                const bGroup = refCountByNodeId.get(bAnchor) ?? 0;
                if (aGroup !== bGroup) return aGroup - bGroup;
                if (aAnchor !== bAnchor) return aAnchor < bAnchor ? -1 : 1;
                // Within the same anchor group, the anchor itself sits last.
                const aIsAnchor = aAnchor === a.id ? 1 : 0;
                const bIsAnchor = bAnchor === b.id ? 1 : 0;
                return aIsAnchor - bIsAnchor;
            });

        const stage1Index = new Map<string, number>();
        stage1.forEach((n, i) => stage1Index.set(n.id, i));

        const symbols = stage1.slice().sort((a, b) => {
            const aR = refCountByNodeId.get(a.id) ?? 0;
            const bR = refCountByNodeId.get(b.id) ?? 0;
            if (aR !== bR) return aR - bR;
            return (stage1Index.get(a.id) ?? 0) - (stage1Index.get(b.id) ?? 0);
        });

        let maxRefCount = 0;
        for (const n of symbols) {
            const c = refCountByNodeId.get(n.id) ?? 0;
            if (c > maxRefCount) maxRefCount = c;
        }

        const fileNodes: GraphRFNode[] = files.map((n, i) => ({
            id: n.id,
            data: { label: n.label, domainNode: n },
            position: {
                x: (i % COLS) * COL_WIDTH,
                y: Math.floor(i / COLS) * ROW_HEIGHT,
            },
            style: {
                ...baseStyle,
                background: FILE_BG,
                ...searchMatchStyle(n.id),
                ...highlightStyle(n.id),
            },
        }));

        const filesRows = Math.ceil(files.length / COLS);
        const symbolsYOffset = filesRows * ROW_HEIGHT + FILE_SYMBOL_GAP;
        const symbolNodes: GraphRFNode[] = symbols.map((n, i) => ({
            id: n.id,
            data: { label: n.label, domainNode: n },
            position: {
                x: (i % COLS) * COL_WIDTH,
                y: symbolsYOffset + Math.floor(i / COLS) * ROW_HEIGHT,
            },
            style: {
                ...baseStyle,
                background: symbolBg(
                    refCountByNodeId.get(n.id) ?? 0,
                    maxRefCount,
                ),
                ...searchMatchStyle(n.id),
                ...highlightStyle(n.id),
            },
        }));

        return [...fileNodes, ...symbolNodes];
    }, [
        visibleDomainNodes,
        refCountByNodeId,
        anchorByNodeId,
        highlightedIds,
        searchMatchedIds,
    ]);

    const flowEdges = useMemo<GraphRFEdge[]>(() => {
        return visibleDomainEdges.map((e) => {
            const highlighted =
                highlightedIds.has(e.from) && highlightedIds.has(e.to);
            const color = highlighted
                ? "#2563eb"
                : e.kind === "defines"
                  ? "#a5b4fc"
                  : "#cbd5e1";
            return {
                id: e.id,
                source: e.from,
                target: e.to,
                selectable: false,
                focusable: false,
                interactionWidth: 0,
                style: { stroke: color },
                markerEnd: {
                    type: MarkerType.ArrowClosed,
                    color,
                    width: 32,
                    height: 32,
                },
            };
        });
    }, [visibleDomainEdges, highlightedIds]);

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
                minZoom={MIN_ZOOM}
                onNodesChange={onNodesChange}
                onEdgesChange={onEdgesChange}
                onNodeClick={(event, node) => {
                    if (event.altKey) {
                        // Alt+click: gather this node's dependants and stack
                        // them in a 5-wide grid right above the clicked node,
                        // filling bottom-up so the row closest to it fills
                        // first. Clicked node stays in place as the anchor.
                        const deps = gatherDependants(
                            node.id,
                            visibleDomainEdges,
                        );
                        const Cx = node.position.x;
                        const Cy = node.position.y;
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
                            return nds.map((n) =>
                                newPos.has(n.id)
                                    ? { ...n, position: newPos.get(n.id)! }
                                    : n,
                            );
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
