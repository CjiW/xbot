import { useEditor, EditorContent } from '@tiptap/react'
import StarterKit from '@tiptap/starter-kit'
import Placeholder from '@tiptap/extension-placeholder'
import CodeBlockLowlight from '@tiptap/extension-code-block-lowlight'
import TaskList from '@tiptap/extension-task-list'
import TaskItem from '@tiptap/extension-task-item'
import { Markdown } from 'tiptap-markdown'
import { common, createLowlight } from 'lowlight'
import { useEffect, useRef, useState, useImperativeHandle, forwardRef } from 'react'

const lowlight = createLowlight(common)

interface TiptapEditorProps {
  onSend: (content: string) => void
  disabled: boolean
  connected: boolean
}

export interface TiptapEditorHandle {
  /** Set editor content (markdown string) and focus at end */
  setContent: (md: string) => void
  /** Focus the editor */
  focus: () => void
}

const TiptapEditor = forwardRef<TiptapEditorHandle, TiptapEditorProps>(
  function TiptapEditor({ onSend, disabled, connected }, ref) {
  const [hasContent, setHasContent] = useState(false)
  const connectedRef = useRef(connected)
  useEffect(() => { connectedRef.current = connected }, [connected])

  const editor = useEditor({
    extensions: [
      StarterKit.configure({
        codeBlock: false,
      }),
      Placeholder.configure({
        placeholder: () => connectedRef.current ? '输入消息...' : '连接中...',
      }),
      CodeBlockLowlight.configure({
        lowlight,
      }),
      TaskList,
      TaskItem.configure({
        nested: true,
      }),
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
        if (event.key === 'Enter' && !event.shiftKey) {
          event.preventDefault()
          handleSend()
          return true
        }
        return false
      },
    },
    editable: !disabled,
    immediatelyRender: false,
    onUpdate: ({ editor: ed }) => {
      setHasContent(ed.getText().trim().length > 0)
    },
  })

  useEffect(() => {
    if (editor) {
      editor.setEditable(!disabled && connected)
    }
  }, [editor, disabled, connected])

  // Expose setContent and focus to parent via ref
  useImperativeHandle(ref, () => ({
    setContent: (md: string) => {
      if (!editor) return
      editor.commands.setContent(md)
      // Move cursor to end
      editor.commands.focus('end')
    },
    focus: () => {
      editor?.commands.focus()
    },
  }), [editor])

  const handleSend = () => {
    if (!editor) return
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    const md = (editor.storage as any).markdown.getMarkdown()
    if (!md.trim()) return
    onSend(md)
    editor.commands.clearContent()
    setHasContent(false)
    editor.commands.focus()
  }

  return (
    <div className="tiptap-wrapper">
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
    </div>
  )
})

export default TiptapEditor
