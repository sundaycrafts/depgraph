import { useState } from 'react'
import { useGraph } from './hooks/useGraph'
import { GraphCanvas } from './components/GraphCanvas/GraphCanvas'
import { CodeViewerPanel } from './components/CodeViewerPanel'
import { SymbolFilter } from './components/SymbolFilter'
import type { Node } from './schemas/api'

export default function App() {
  const { data: graph, isLoading, error } = useGraph()
  const [selectedNode, setSelectedNode] = useState<Node | null>(null)
  const [selectedKinds, setSelectedKinds] = useState<string[]>([])
  const [limitToHundred, setLimitToHundred] = useState<boolean>(true)

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
      <div className="flex-1 flex flex-col min-h-0">
        <div className="border-b px-3 py-2 flex items-center gap-3 shrink-0 bg-white">
          <span className="text-xs text-gray-500 shrink-0">Symbols</span>
          <SymbolFilter
            graph={graph}
            selectedKinds={selectedKinds}
            onKindsChange={setSelectedKinds}
          />
          <label className="flex items-center gap-1 text-xs text-gray-700 shrink-0">
            <input
              type="checkbox"
              checked={limitToHundred}
              onChange={(e) => setLimitToHundred(e.target.checked)}
            />
            Limit to 100 nodes
          </label>
        </div>
        <div className="flex-1 min-h-0">
          <GraphCanvas
            graph={graph}
            onNodeSelect={setSelectedNode}
            selectedKinds={selectedKinds}
            limitToHundred={limitToHundred}
          />
        </div>
      </div>
      <div className="w-96 border-l">
        <CodeViewerPanel node={selectedNode} />
      </div>
    </div>
  )
}
