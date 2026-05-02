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
import { useGroupDrag } from "./useGroupDrag";

interface Props {
    graph: Graph;
    onNodeSelect: (node: DomainNode) => void;
    selectedKinds: string[];
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
const COLS = 6;
const COL_WIDTH = 160;
const ROW_HEIGHT = 36;
const FILE_SYMBOL_GAP = 10;

// symbolBg interpolates the symbol node background from yellow toward red as
// the incoming reference count grows. Hue is the dominant change; saturation
// and lightness drop only slightly so dark text stays readable.
function symbolBg(refCount: number, maxRefCount: number): string {
    if (maxRefCount === 0) {
        return `hsl(${SYMBOL_BASE_HSL.h}, ${SYMBOL_BASE_HSL.s}%, ${SYMBOL_BASE_HSL.l}%)`;
    }
    const t = Math.min(refCount / maxRefCount, 1);
    const h = SYMBOL_BASE_HSL.h + (SYMBOL_HOT_HSL.h - SYMBOL_BASE_HSL.h) * t;
    const s = SYMBOL_BASE_HSL.s + (SYMBOL_HOT_HSL.s - SYMBOL_BASE_HSL.s) * t;
    const l = SYMBOL_BASE_HSL.l + (SYMBOL_HOT_HSL.l - SYMBOL_BASE_HSL.l) * t;
    return `hsl(${h.toFixed(1)}, ${s.toFixed(1)}%, ${l.toFixed(1)}%)`;
}

function GraphCanvasInner({ graph, onNodeSelect, selectedKinds }: Props) {
    const { fitView } = useReactFlow<GraphRFNode, GraphRFEdge>();
    const [selectedNodeId, setSelectedNodeId] = useState<string | null>(null);

    const visibleDomainNodes = useMemo(() => {
        return graph.nodes.filter((n) => {
            if (n.kind === "file") {
                return selectedKinds.includes("file");
            }
            return (
                selectedKinds.length === 0 ||
                (n.symbolKind != null && selectedKinds.includes(n.symbolKind))
            );
        });
    }, [graph.nodes, selectedKinds]);

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

    const flowNodes = useMemo<GraphRFNode[]>(() => {
        const baseStyle = {
            border: "1px solid #94a3b8",
            borderRadius: "6px",
            fontSize: "12px",
            padding: "4px 8px",
        };
        const highlightStyle = (id: string) =>
            highlightedIds.has(id) ? { background: HIGHLIGHT_BG } : {};

        const files = visibleDomainNodes.filter((n) => n.kind === "file");
        const symbols = visibleDomainNodes
            .filter((n) => n.kind === "symbol")
            .slice()
            .sort(
                (a, b) =>
                    (refCountByNodeId.get(a.id) ?? 0) -
                    (refCountByNodeId.get(b.id) ?? 0),
            );

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
                ...highlightStyle(n.id),
            },
        }));

        return [...fileNodes, ...symbolNodes];
    }, [visibleDomainNodes, refCountByNodeId, highlightedIds]);

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
            fitView({ padding: 0.2 });
        });
    }, [nodes.length, edges.length, fitView]);

    const { onNodeDragStart, onNodeDrag, onNodeDragStop } =
        useGroupDrag(highlightedIds);

    return (
        <div style={{ width: "100%", height: "100%" }}>
            <ReactFlow
                nodes={nodes}
                edges={edges}
                onNodesChange={onNodesChange}
                onEdgesChange={onEdgesChange}
                onNodeClick={(_, node) => {
                    setSelectedNodeId((prev) =>
                        prev === node.id ? null : node.id,
                    );
                    onNodeSelect(node.data.domainNode);
                }}
                onPaneClick={() => setSelectedNodeId(null)}
                onNodeDragStart={onNodeDragStart}
                onNodeDrag={onNodeDrag}
                onNodeDragStop={onNodeDragStop}
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
