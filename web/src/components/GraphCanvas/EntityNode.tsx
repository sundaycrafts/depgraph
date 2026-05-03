import type { Node, NodeProps } from "@xyflow/react";
import type { Node as DomainNode } from "../../schemas/api";

export type EntityNodeData = {
    label: string;
    header: string;
    bg: string;
    domainNode: DomainNode;
};
export type EntityNodeType = Node<EntityNodeData, "entityNode">;

export function EntityNode({ data }: NodeProps<EntityNodeType>) {
    return (
        <div
            style={{
                width: "100%",
                height: "100%",
                position: "relative",
                background: data.bg,
                border: "1px solid #94a3b8",
                borderRadius: 6,
                overflow: "hidden",
            }}
        >
            <div
                style={{
                    position: "absolute",
                    top: 1,
                    left: 4,
                    fontSize: 9,
                    color: "#475569",
                    pointerEvents: "none",
                    whiteSpace: "nowrap",
                    maxWidth: "calc(100% - 8px)",
                    overflow: "hidden",
                    textOverflow: "ellipsis",
                }}
            >
                {data.header}
            </div>
            <div
                style={{
                    paddingTop: 12,
                    paddingLeft: 4,
                    paddingRight: 4,
                    fontSize: 11,
                    lineHeight: 1.15,
                    whiteSpace: "normal",
                    overflowWrap: "anywhere",
                }}
            >
                {data.label}
            </div>
        </div>
    );
}
