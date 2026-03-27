import { useEditor, EditorContent } from '@tiptap/react'
import StarterKit from '@tiptap/starter-kit'
import Placeholder from '@tiptap/extension-placeholder'
import CodeBlockLowlight from '@tiptap/extension-code-block-lowlight'
import TaskList from '@tiptap/extension-task-list'
import TaskItem from '@tiptap/extension-task-item'
import { Markdown } from 'tiptap-markdown'
import { common, createLowlight } from 'lowlight'
import { useEffect } from 'react'

const lowlight = createLowlight(common)

interface TiptapEditorProps {
  onSend: (content: string) => void
  disabled: boolean
  connected: boolean
}

export default function TiptapEditor({ onSend, disabled, connected }: TiptapEditorProps) {
  const editor = useEditor({
    extensions: [
      StarterKit.configure({
        codeBlock: false, // We use CodeBlockLowlight instead
      }),
      Placeholder.configure({
        placeholder: connected ? '输入消息...' : '连接中...',
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
  })

  useEffect(() => {
    if (editor) {
      editor.setEditable(!disabled && connected)
    }
  }, [editor, disabled, connected])

  const handleSend = () => {
    if (!editor) return
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    const md = (editor.storage as any).markdown.getMarkdown()
    if (!md.trim()) return
    onSend(md)
    editor.commands.clearContent()
    editor.commands.focus()
  }

  return (
    <div className="tiptap-wrapper">
      <div style={{ flex: 1, minWidth: 0 }}>
        <EditorContent editor={editor} />
      </div>
      <button
        onClick={handleSend}
        disabled={!connected || !editor?.isEditable || !editor.getText().trim()}
        className="tiptap-send-btn"
        title="发送"
      >
        ➤
      </button>
    </div>
  )
}
