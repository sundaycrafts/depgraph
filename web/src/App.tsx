import { useState, useEffect } from 'react'
import { GraphCanvas } from './components/GraphCanvas'
import { CodeViewerPanel } from './components/CodeViewerPanel'
import type { components } from './gen/api'

type Graph = components['schemas']['Graph']
type Node = components['schemas']['Node']

export default function App() {
  const [graph, setGraph] = useState<Graph | null>(null)
  const [selectedNode, _setSelectedNode] = useState<Node | null>(null)

  useEffect(() => {
    fetch('/graph')
      .then((r) => r.json())
      .then((data: Graph) => setGraph(data))
      .catch(console.error)
  }, [])

  if (!graph) return <div>Loading...</div>

  return (
    <div style={{ display: 'flex', height: '100vh' }}>
      <div style={{ flex: 1 }}>
        <GraphCanvas graph={graph} />
      </div>
      <div style={{ width: 400 }}>
        <CodeViewerPanel node={selectedNode} />
      </div>
    </div>
  )
}
