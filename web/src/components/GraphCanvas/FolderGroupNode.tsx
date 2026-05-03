import type { Node, NodeProps } from "@xyflow/react";

type FolderGroupData = { folder: string };
export type FolderGroupNodeType = Node<FolderGroupData, "folderGroup">;

export function FolderGroupNode({
    data,
}: NodeProps<FolderGroupNodeType>) {
    return (
        <div
            style={{
                width: "100%",
                height: "100%",
                border: "1px dashed #94a3b8",
                borderRadius: 8,
                background: "transparent",
                position: "relative",
            }}
        >
            <div
                style={{
                    position: "absolute",
                    top: 2,
                    left: 6,
                    fontSize: 11,
                    color: "#475569",
                    pointerEvents: "none",
                    whiteSpace: "nowrap",
                }}
            >
                {data.folder}
            </div>
        </div>
    );
}
