import { ReactFlow, type Node as RFNode, type Edge as RFEdge, ReactFlowProvider } from '@xyflow/react'
import '@xyflow/react/dist/style.css'
import type { Node, Graph } from '../schemas/api'

interface Props {
  graph: Graph
  onNodeSelect: (node: Node) => void
}

function GraphCanvasInner({ graph, onNodeSelect }: Props) {
  const nodes: RFNode[] = graph.nodes.map((n, i) => ({
    id: n.id,
    data: { label: n.label, domainNode: n },
    position: { x: (i % 6) * 200, y: Math.floor(i / 6) * 100 },
    style: {
      background: n.kind === 'file' ? '#dbeafe' : '#fef9c3',
      border: '1px solid #94a3b8',
      borderRadius: '6px',
      fontSize: '12px',
      padding: '4px 8px',
    },
  }))

  const edges: RFEdge[] = graph.edges.map(e => ({
    id: e.id,
    source: e.from,
    target: e.to,
    label: e.kind,
    style: { stroke: e.kind === 'defines' ? '#6366f1' : '#94a3b8' },
  }))

  return (
    <ReactFlow
      nodes={nodes}
      edges={edges}
      onNodeClick={(_, node) => onNodeSelect(node.data.domainNode as Node)}
      fitView
    />
  )
}

export function GraphCanvas(props: Props) {
  return (
    <ReactFlowProvider>
      <GraphCanvasInner {...props} />
    </ReactFlowProvider>
  )
}
