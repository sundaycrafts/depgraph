import { render, screen } from '@testing-library/react'
import { describe, it, expect } from 'vitest'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { CodeViewerPanel, toMonacoRange } from './CodeViewerPanel'

function wrapper({ children }: { children: React.ReactNode }) {
  return (
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      {children}
    </QueryClientProvider>
  )
}

describe('CodeViewerPanel', () => {
  it('shows placeholder when no node selected', () => {
    render(<CodeViewerPanel node={null} />, { wrapper })
    expect(screen.getByText(/select a node/i)).toBeInTheDocument()
  })

  it('renders without crashing when node is provided', () => {
    const node = { id: 'n1', kind: 'symbol' as const, label: 'MyFunc', path: '/src/main.go' }
    render(<CodeViewerPanel node={node} />, { wrapper })
  })
})

describe('toMonacoRange', () => {
  it('converts 0-based LSP range to 1-based Monaco range', () => {
    expect(toMonacoRange({ start: { line: 0, character: 5 }, end: { line: 0, character: 8 } }))
      .toEqual({ startLineNumber: 1, startColumn: 6, endLineNumber: 1, endColumn: 9 })
  })

  it('converts multi-line range', () => {
    expect(toMonacoRange({ start: { line: 9, character: 0 }, end: { line: 11, character: 3 } }))
      .toEqual({ startLineNumber: 10, startColumn: 1, endLineNumber: 12, endColumn: 4 })
  })
})
