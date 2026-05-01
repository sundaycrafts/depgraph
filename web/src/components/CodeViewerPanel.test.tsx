import { render, screen } from '@testing-library/react'
import { describe, it } from 'vitest'
import { CodeViewerPanel } from './CodeViewerPanel'

describe('CodeViewerPanel', () => {
  it('renders nothing when node is null', () => {
    const { container } = render(<CodeViewerPanel node={null} />)
    if (container.firstChild !== null) {
      throw new Error('expected no rendered output for null node')
    }
  })

  it('displays node label', () => {
    const node = { id: 'n1', kind: 'symbol' as const, label: 'MyFunc' }
    render(<CodeViewerPanel node={node} />)
    screen.getByText(/MyFunc/)
  })
})
