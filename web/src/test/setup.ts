import '@testing-library/jest-dom/vitest'

// ResizeObserver is used by React Flow but not available in jsdom.
;(globalThis as unknown as Record<string, unknown>).ResizeObserver = class {
  observe() {}
  unobserve() {}
  disconnect() {}
}
