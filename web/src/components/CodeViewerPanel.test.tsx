import { render, screen } from '@testing-library/react'
import { describe, it, expect } from 'vitest'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { CodeViewerPanel } from './CodeViewerPanel'

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
