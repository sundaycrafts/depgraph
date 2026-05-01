import { useState } from 'react'
import { useGraph } from './hooks/useGraph'
import { GraphCanvas } from './components/GraphCanvas'
import { CodeViewerPanel } from './components/CodeViewerPanel'
import type { Node } from './schemas/api'

export default function App() {
  const { data: graph, isLoading, error } = useGraph()
  const [selectedNode, setSelectedNode] = useState<Node | null>(null)

  if (isLoading) {
    return (
      <div className="flex items-center justify-center h-screen text-gray-500">
        Loading graph...
      </div>
    )
  }

  if (error) {
    return (
      <div className="flex items-center justify-center h-screen text-red-500">
        Error: {error.message}
      </div>
    )
  }

  if (!graph) return null

  return (
    <div className="flex h-screen">
      <div className="flex-1">
        <GraphCanvas graph={graph} onNodeSelect={setSelectedNode} />
      </div>
      <div className="w-96 border-l">
        <CodeViewerPanel node={selectedNode} />
      </div>
    </div>
  )
}
