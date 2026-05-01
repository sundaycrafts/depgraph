import { render, screen } from '@testing-library/react'
import { describe, it } from 'vitest'
import { GraphCanvas } from './GraphCanvas'

describe('GraphCanvas', () => {
  it('displays node count', () => {
    const graph = {
      nodes: [{ id: '1', kind: 'file' as const, label: 'foo.ts' }],
      edges: [],
    }
    render(<GraphCanvas graph={graph} />)
    screen.getByText(/1 nodes/)
  })

  it('renders with empty graph', () => {
    render(<GraphCanvas graph={{ nodes: [], edges: [] }} />)
    screen.getByText(/0 nodes/)
  })
})
