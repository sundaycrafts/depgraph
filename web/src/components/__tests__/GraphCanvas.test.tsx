import { render } from '@testing-library/react'
import { GraphCanvas } from '../GraphCanvas/GraphCanvas'
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

// 200-node graph for exercising the 100-node display cap.
const largeGraph: Graph = {
  nodes: Array.from({ length: 200 }, (_, i) => ({
    id: `n${i}`,
    kind: i % 2 === 0 ? 'file' : 'symbol',
    label: `node-${i}`,
    path: `/src/file-${i}.ts`,
  })),
  edges: [],
}

test('renders without crashing on empty graph', () => {
  render(<GraphCanvas graph={emptyGraph} onNodeSelect={() => {}} selectedKinds={[]} />)
})

test('renders without crashing with nodes and edges', () => {
  render(<GraphCanvas graph={smallGraph} onNodeSelect={() => {}} selectedKinds={[]} />)
})

test('renders without crashing with 200-node graph + limitToHundred=true', () => {
  render(
    <GraphCanvas
      graph={largeGraph}
      onNodeSelect={() => {}}
      selectedKinds={['file']}
      limitToHundred
    />
  )
})

test('renders without crashing with 200-node graph + limitToHundred=false', () => {
  render(
    <GraphCanvas
      graph={largeGraph}
      onNodeSelect={() => {}}
      selectedKinds={['file']}
      limitToHundred={false}
    />
  )
})
