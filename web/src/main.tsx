import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import './index.css'
import App from './App.tsx'

// Retry aggressively so the UI keeps waiting while the Go backend compiles
// and runs LSP analysis before its HTTP server becomes available.
const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      retry: 60,
      retryDelay: 2000,
    },
  },
})

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <App />
    </QueryClientProvider>
  </StrictMode>,
)
