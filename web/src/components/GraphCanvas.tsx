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

    const flowNodes = useMemo<GraphRFNode[]>(() => {
        return visibleDomainNodes.map((n, i) => ({
            id: n.id,
            data: {
                label: n.label,
                domainNode: n,
            },
            position: {
                x: (i % 6) * 200,
                y: Math.floor(i / 6) * 100,
            },
            style: {
                background: n.kind === "file" ? "#dbeafe" : "#fef9c3",
                border: "1px solid #94a3b8",
                borderRadius: "6px",
                fontSize: "12px",
                padding: "4px 8px",
            },
        }));
    }, [visibleDomainNodes]);

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
