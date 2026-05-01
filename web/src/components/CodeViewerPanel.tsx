import Editor from '@monaco-editor/react'
import { useFile } from '../hooks/useFile'
import type { Node } from '../schemas/api'

interface Props {
  node: Node | null
}

export function CodeViewerPanel({ node }: Props) {
  const { data, isLoading } = useFile(node?.path)

  if (!node) {
    return (
      <div className="flex items-center justify-center h-full text-gray-400 text-sm p-4">
        Select a node to view source
      </div>
    )
  }

  if (isLoading) {
    return (
      <div className="flex items-center justify-center h-full text-gray-400 text-sm p-4">
        Loading...
      </div>
    )
  }

  const language = node.path?.endsWith('.go') ? 'go' : 'plaintext'

  return (
    <div className="h-full flex flex-col">
      <div className="text-xs text-gray-500 px-3 py-1 border-b truncate bg-gray-50">
        {node.path ?? node.label}
      </div>
      <div className="flex-1">
        <Editor
          height="100%"
          language={language}
          value={data?.content ?? ''}
          options={{ readOnly: true, minimap: { enabled: false }, fontSize: 13 }}
        />
      </div>
    </div>
  )
}
