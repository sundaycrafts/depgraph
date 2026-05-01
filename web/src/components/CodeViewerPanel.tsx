import { useRef, useEffect } from 'react'
import Editor, { type OnMount } from '@monaco-editor/react'
import { useFile } from '../hooks/useFile'
import type { Node, Range } from '../schemas/api'

type MonacoEditor = Parameters<OnMount>[0]
type DecorationsCollection = ReturnType<MonacoEditor['createDecorationsCollection']>

interface Props {
  node: Node | null
}

export function toMonacoRange(r: Range) {
  return {
    startLineNumber: r.start.line + 1,
    startColumn:     r.start.character + 1,
    endLineNumber:   r.end.line + 1,
    endColumn:       r.end.character + 1,
  }
}

export function CodeViewerPanel({ node }: Props) {
  const { data, isLoading } = useFile(node?.path)
  const editorRef = useRef<MonacoEditor | null>(null)
  const decorationsRef = useRef<DecorationsCollection | null>(null)

  const handleMount: OnMount = (ed) => { editorRef.current = ed }

  useEffect(() => {
    const ed = editorRef.current
    if (!ed || !node?.range) {
      decorationsRef.current?.clear()
      return
    }
    const mr = toMonacoRange(node.range)
    decorationsRef.current?.clear()
    decorationsRef.current = ed.createDecorationsCollection([{
      range: mr,
      options: { className: 'dp-range-highlight', isWholeLine: false },
    }])
    ed.revealRangeInCenter(mr)
  }, [node, data?.content])

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
          onMount={handleMount}
          options={{ readOnly: true, minimap: { enabled: false }, fontSize: 13 }}
        />
      </div>
    </div>
  )
}
