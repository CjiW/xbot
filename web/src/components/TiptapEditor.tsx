import { useEditor, EditorContent } from '@tiptap/react'
import StarterKit from '@tiptap/starter-kit'
import Placeholder from '@tiptap/extension-placeholder'
import CodeBlockLowlight from '@tiptap/extension-code-block-lowlight'
import TaskList from '@tiptap/extension-task-list'
import TaskItem from '@tiptap/extension-task-item'
import { Markdown } from 'tiptap-markdown'
import { common, createLowlight } from 'lowlight'
import { useEffect, useRef, useState, useImperativeHandle, useCallback, forwardRef } from 'react'

const lowlight = createLowlight(common)

const MAX_HISTORY = 100

interface TiptapEditorProps {
  onSend: (content: string) => void
  disabled: boolean
  connected: boolean
  currentModel?: string
  onCancel?: () => void
}

export interface TiptapEditorHandle {
  setContent: (md: string) => void
  focus: () => void
}

const TiptapEditor = forwardRef<TiptapEditorHandle, TiptapEditorProps>(
  function TiptapEditor({ onSend, disabled, connected, currentModel, onCancel }, ref) {
  const [hasContent, setHasContent] = useState(false)
  const connectedRef = useRef(connected)
  useEffect(() => { connectedRef.current = connected }, [connected])

  // Input history (newest first, like bash history)
  const inputHistoryRef = useRef<string[]>([])
  const historyIndexRef = useRef(-1)

  const editor = useEditor({
    extensions: [
      StarterKit.configure({ codeBlock: false }),
      Placeholder.configure({
        placeholder: () => connectedRef.current ? '输入消息... (Shift+Enter 换行)' : '连接中...',
      }),
      CodeBlockLowlight.configure({ lowlight }),
      TaskList,
      TaskItem.configure({ nested: true }),
      Markdown.configure({
        html: false,
        breaks: true,
        transformPastedText: true,
        transformCopiedText: true,
      }),
    ],
    content: '',
    editorProps: {
      attributes: {
        class: 'tiptap-editor max-w-none focus:outline-none',
      },
      handleKeyDown: (_view, event) => {
        // Enter = send (without Shift/Ctrl/Cmd)
        if (event.key === 'Enter' && !event.shiftKey && !(event.ctrlKey || event.metaKey)) {
          event.preventDefault()
          handleSend()
          return true
        }

        // Ctrl/Cmd+Enter = newline (let tiptap handle natively)
        if (event.key === 'Enter' && (event.ctrlKey || event.metaKey)) {
          return false
        }

        // Escape = cancel or clear
        if (event.key === 'Escape') {
          if (onCancel) {
            onCancel()
          } else {
            editor?.commands.clearContent()
            setHasContent(false)
          }
          return true
        }

        // ArrowUp = browse history (when editor is empty)
        if (event.key === 'ArrowUp' && !event.shiftKey && !event.ctrlKey && !event.metaKey) {
          if (editor) {
            const text = editor.getText().trim()
            if (text === '' && inputHistoryRef.current.length > 0) {
              const idx = historyIndexRef.current + 1
              if (idx < inputHistoryRef.current.length) {
                historyIndexRef.current = idx
                editor.commands.setContent(inputHistoryRef.current[idx])
                setHasContent(true)
                editor.commands.focus('end')
                return true
              }
            }
          }
        }

        // ArrowDown = browse history forward
        if (event.key === 'ArrowDown' && !event.shiftKey && !event.ctrlKey && !event.metaKey) {
          if (editor && historyIndexRef.current >= 0) {
            const idx = historyIndexRef.current - 1
            if (idx >= 0) {
              historyIndexRef.current = idx
              editor.commands.setContent(inputHistoryRef.current[idx])
              editor.commands.focus('end')
              return true
            } else {
              historyIndexRef.current = -1
              editor.commands.clearContent()
              setHasContent(false)
              return true
            }
          }
        }

        return false
      },
    },
    editable: !disabled,
    immediatelyRender: false,
    onUpdate: ({ editor: ed }) => {
      setHasContent(ed.getText().trim().length > 0)
      // Exit history browsing mode on any content change
      historyIndexRef.current = -1
    },
  })

  useEffect(() => {
    if (editor) {
      editor.setEditable(!disabled && connected)
    }
  }, [editor, disabled, connected])

  useImperativeHandle(ref, () => ({
    setContent: (md: string) => {
      if (!editor) return
      editor.commands.setContent(md)
      editor.commands.focus('end')
    },
    focus: () => {
      editor?.commands.focus()
    },
  }), [editor])

  const handleSend = useCallback(() => {
    if (!editor) return
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    const md = (editor.storage as any).markdown.getMarkdown()
    if (!md.trim()) return

    // Add to history (deduplicate with first entry)
    const history = inputHistoryRef.current
    if (history.length === 0 || history[0] !== md) {
      history.unshift(md)
      if (history.length > MAX_HISTORY) {
        history.length = MAX_HISTORY
      }
    }
    historyIndexRef.current = -1

    onSend(md)
    editor.commands.clearContent()
    setHasContent(false)
    editor.commands.focus()
  }, [editor, onSend])

  return (
    <div className="tiptap-wrapper relative">
      <div style={{ flex: 1, minWidth: 0 }}>
        <EditorContent editor={editor} />
      </div>
      <button
        onClick={handleSend}
        disabled={!connected || disabled || !hasContent}
        className="tiptap-send-btn"
        title="发送"
      >
        ➤
      </button>
      {currentModel && (
        <span className="absolute bottom-1 left-3 text-[11px] text-slate-500 truncate pointer-events-none select-none">
          {currentModel}
        </span>
      )}
    </div>
  )
})

export default TiptapEditor
