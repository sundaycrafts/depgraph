import { useCallback, useRef } from "react";
import { useReactFlow, type OnNodeDrag } from "@xyflow/react";
import type { GraphRFNode } from "./GraphCanvas";

type DragState = {
    nodeId: string;
    startX: number;
    startY: number;
    peers: Map<string, { x: number; y: number }>;
};

export function useGroupDrag(highlightedIds: Set<string>) {
    const { getNodes, setNodes } = useReactFlow<GraphRFNode>();
    const dragRef = useRef<DragState | null>(null);

    const onNodeDragStart: OnNodeDrag<GraphRFNode> = useCallback(
        (event, node) => {
            if (!(event.altKey && event.shiftKey) || !highlightedIds.has(node.id)) {
                dragRef.current = null;
                return;
            }
            const peers = new Map<string, { x: number; y: number }>();
            for (const n of getNodes()) {
                if (highlightedIds.has(n.id) && n.id !== node.id)
                    peers.set(n.id, { x: n.position.x, y: n.position.y });
            }
            dragRef.current = {
                nodeId: node.id,
                startX: node.position.x,
                startY: node.position.y,
                peers,
            };
        },
        [highlightedIds, getNodes],
    );

    const onNodeDrag: OnNodeDrag<GraphRFNode> = useCallback(
        (_, node) => {
            const s = dragRef.current;
            if (!s || s.nodeId !== node.id) return;
            const dx = node.position.x - s.startX;
            const dy = node.position.y - s.startY;
            setNodes((nds) =>
                nds.map((n) => {
                    const start = s.peers.get(n.id);
                    return start
                        ? { ...n, position: { x: start.x + dx, y: start.y + dy } }
                        : n;
                }),
            );
        },
        [setNodes],
    );

    const onNodeDragStop: OnNodeDrag<GraphRFNode> = useCallback(() => {
        dragRef.current = null;
    }, []);

    return { onNodeDragStart, onNodeDrag, onNodeDragStop };
}
