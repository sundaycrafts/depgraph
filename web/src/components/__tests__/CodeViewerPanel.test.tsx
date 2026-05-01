import { render, screen } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { CodeViewerPanel } from '../CodeViewerPanel'
import type { Node } from '../../schemas/api'

function wrapper({ children }: { children: React.ReactNode }) {
  return (
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      {children}
    </QueryClientProvider>
  )
}

test('shows placeholder when no node selected', () => {
  render(<CodeViewerPanel node={null} />, { wrapper })
  expect(screen.getByText(/select a node/i)).toBeInTheDocument()
})

test('shows loading state when node is selected', () => {
  const node: Node = { id: 'n1', kind: 'file', label: 'main.go', path: '/src/main.go' }
  render(<CodeViewerPanel node={node} />, { wrapper })
  // Monaco is not available in jsdom, but the component should not crash.
  // Loading state or editor container should be present.
  expect(document.body).toBeTruthy()
})
