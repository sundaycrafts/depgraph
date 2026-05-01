import {
    ReactFlow,
    ReactFlowProvider,
    useEdgesState,
    useNodesState,
    useReactFlow,
    type Edge as RFEdge,
    type Node as RFNode,
} from "@xyflow/react";
import { useEffect, useMemo } from "react";
import "@xyflow/react/dist/style.css";

import type { Graph, Node as DomainNode } from "../schemas/api";

interface Props {
    graph: Graph;
    onNodeSelect: (node: DomainNode) => void;
    selectedKinds: string[];
}

type GraphNodeData = {
    label: string;
    domainNode: DomainNode;
};

type GraphRFNode = RFNode<GraphNodeData>;
type GraphRFEdge = RFEdge;

const FILE_BG = "#dbeafe";
const SYMBOL_BASE_HSL = { h: 53, s: 96, l: 88 } as const; // #fef9c3
const SYMBOL_HOT_HSL = { h: 0, s: 85, l: 82 } as const; // max-references red
const COLS = 6;
const COL_WIDTH = 200;
const ROW_HEIGHT = 100;
const FILE_SYMBOL_GAP = 40;

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
            style: { ...baseStyle, background: FILE_BG },
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
            },
        }));

        return [...fileNodes, ...symbolNodes];
    }, [visibleDomainNodes, refCountByNodeId]);

    const flowEdges = useMemo<GraphRFEdge[]>(() => {
        return visibleDomainEdges.map((e) => ({
            id: e.id,
            source: e.from,
            target: e.to,
            label: e.kind,
            style: {
                stroke: e.kind === "defines" ? "#6366f1" : "#94a3b8",
            },
        }));
    }, [visibleDomainEdges]);

    const [nodes, setNodes, onNodesChange] =
        useNodesState<GraphRFNode>(flowNodes);
    const [edges, setEdges, onEdgesChange] =
        useEdgesState<GraphRFEdge>(flowEdges);

    useEffect(() => {
        setNodes(flowNodes);
    }, [flowNodes, setNodes]);

    useEffect(() => {
        setEdges(flowEdges);
    }, [flowEdges, setEdges]);

    useEffect(() => {
        window.requestAnimationFrame(() => {
            fitView({ padding: 0.2 });
        });
    }, [nodes.length, edges.length, fitView]);

    return (
        <div style={{ width: "100%", height: "100%" }}>
            <ReactFlow
                nodes={nodes}
                edges={edges}
                onNodesChange={onNodesChange}
                onEdgesChange={onEdgesChange}
                onNodeClick={(_, node) => {
                    onNodeSelect(node.data.domainNode);
                }}
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
