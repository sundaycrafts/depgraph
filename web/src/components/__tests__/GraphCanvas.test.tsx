import { render } from '@testing-library/react'
import { GraphCanvas } from '../GraphCanvas'
import type { Graph } from '../../schemas/api'

const emptyGraph: Graph = { nodes: [], edges: [] }

const smallGraph: Graph = {
  nodes: [
    { id: 'n1', kind: 'file', label: 'main.go', path: '/src/main.go' },
    { id: 'n2', kind: 'symbol', label: 'main', path: '/src/main.go' },
  ],
  edges: [
    { id: 'e1', from: 'n1', to: 'n2', kind: 'defines', confidence: 'exact' },
  ],
}

test('renders without crashing on empty graph', () => {
  render(<GraphCanvas graph={emptyGraph} onNodeSelect={() => {}} />)
})

test('renders without crashing with nodes and edges', () => {
  render(<GraphCanvas graph={smallGraph} onNodeSelect={() => {}} />)
})
