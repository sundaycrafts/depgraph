import { render } from '@testing-library/react'
import { describe, it } from 'vitest'
import { GraphCanvas } from './GraphCanvas'

describe('GraphCanvas', () => {
  it('renders without crashing on empty graph', () => {
    render(<GraphCanvas graph={{ nodes: [], edges: [] }} onNodeSelect={() => {}} />)
  })

  it('renders with nodes and edges', () => {
    const graph = {
      nodes: [{ id: 'n1', kind: 'file' as const, label: 'main.go', path: '/src/main.go' }],
      edges: [],
    }
    render(<GraphCanvas graph={graph} onNodeSelect={() => {}} />)
  })
})
