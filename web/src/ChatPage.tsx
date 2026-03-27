import { useEffect, useRef, useState, useCallback } from 'react'
import Markdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import ProgressPanel from './components/ProgressPanel'
import type { WsProgressPayload } from './components/ProgressPanel'
import TiptapEditor from './components/TiptapEditor'
import { getCodeBlockProps } from './components/CodeBlock'
import SettingsPanel from './components/SettingsPanel'
import FileUpload, { uploadFile, usePasteUpload, type PendingFile } from './components/FileUpload'

interface ChatPageProps {
  onLogout: () => void
}

interface Message {
  id: string
  type: 'user' | 'assistant' | 'system'
  content: string
  ts?: number
}

const codeBlockComponents = getCodeBlockProps()

function formatTime(ts: number): string {
  return new Date(ts * 1000).toLocaleTimeString('zh-CN', {
    hour: '2-digit',
    minute: '2-digit',
  })
}

function formatFileSize(bytes: number): string {
  if (bytes < 1024) return bytes + ' B'
  if (bytes < 1024 * 1024) return (bytes / 1024).toFixed(1) + ' KB'
  return (bytes / (1024 * 1024)).toFixed(1) + ' MB'
}

export default function ChatPage({ onLogout }: ChatPageProps) {
  const [messages, setMessages] = useState<Message[]>([])
  const [connected, setConnected] = useState(false)
  const [loading, setLoading] = useState(false)
  const [progress, setProgress] = useState<WsProgressPayload | null>(null)
  const [autoScroll, setAutoScroll] = useState(true)
  const [reconnecting, setReconnecting] = useState(false)
  const [settingsOpen, setSettingsOpen] = useState(false)
  const [pendingFiles, setPendingFiles] = useState<PendingFile[]>([])
  const [dragActive, setDragActive] = useState(false)

  const wsRef = useRef<WebSocket | null>(null)
  const messagesContainerRef = useRef<HTMLDivElement>(null)
  const messagesEndRef = useRef<HTMLDivElement>(null)
  const reconnectDelayRef = useRef(1000)
  const reconnectTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const serverStopped = useRef(false)

  // --- Scroll management ---
  const scrollToBottom = useCallback(() => {
    messagesEndRef.current?.scrollIntoView({ behavior: 'smooth' })
  }, [])

  const handleContainerScroll = useCallback(() => {
    const el = messagesContainerRef.current
    if (!el) return
    const distFromBottom = el.scrollHeight - el.scrollTop - el.clientHeight
    if (distFromBottom > 150) {
      setAutoScroll(false)
    } else {
      setAutoScroll(true)
    }
  }, [])

  // Auto-scroll when new messages arrive (if autoScroll is on)
  useEffect(() => {
    if (autoScroll) {
      scrollToBottom()
    }
  }, [messages, progress, autoScroll, scrollToBottom])

  // --- Load history on mount ---
  useEffect(() => {
    fetch('/api/history?limit=50')
      .then((r) => r.json())
      .then((data) => {
        if (data.ok && data.messages) {
          const hist: Message[] = data.messages.map((m: { role: string; content: string }, i: number) => ({
            id: `hist-${i}`,
            type: m.role === 'user' ? 'user' : m.role === 'assistant' ? 'assistant' : 'system',
            content: m.content,
          }))
          setMessages(hist)
          setTimeout(scrollToBottom, 100)
        }
      })
      .catch(() => {})
  }, [scrollToBottom])

  // --- WebSocket connection with reconnect ---
  const connectWS = useCallback(() => {
    const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
    const wsUrl = `${protocol}//${window.location.host}/ws`
    const ws = new WebSocket(wsUrl)
    wsRef.current = ws

    ws.onopen = () => {
      setConnected(true)
      setReconnecting(false)
      serverStopped.current = false
      reconnectDelayRef.current = 1000
      if (reconnectTimerRef.current) {
        clearTimeout(reconnectTimerRef.current)
        reconnectTimerRef.current = null
      }
    }

    ws.onclose = (e) => {
      setConnected(false)

      // Normal closure (1000) or going away (1001) = server shutdown, don't reconnect
      if (e.code === 1000 || e.code === 1001) {
        serverStopped.current = true
        setReconnecting(false)
        return
      }

      setReconnecting(true)

      // Exponential backoff reconnect
      if (reconnectTimerRef.current) {
        clearTimeout(reconnectTimerRef.current)
      }
      reconnectTimerRef.current = setTimeout(() => {
        connectWS()
      }, reconnectDelayRef.current)
      reconnectDelayRef.current = Math.min(reconnectDelayRef.current * 2, 30000)
    }

    ws.onmessage = (e) => {
      try {
        const data = JSON.parse(e.data)

        switch (data.type) {
          case 'progress':
            // Legacy progress (string content) — keep loading, show dots
            setLoading(true)
            break

          case 'progress_structured':
            // Structured progress — update progress panel
            setProgress(data.progress)
            setLoading(true)
            break

          case 'text':
          case 'card': {
            // Final message — clear progress, add to messages
            setProgress(null)
            setLoading(false)
            const msg: Message = {
              id: data.id || `ws-${Date.now()}`,
              type: data.type === 'card' ? 'system' : 'assistant',
              content: data.content,
              ts: data.ts,
            }
            setMessages((prev) => [...prev, msg])
            break
          }

          case 'file': {
            setProgress(null)
            setLoading(false)
            const fileMsg: Message = {
              id: data.id || `file-${Date.now()}`,
              type: 'system',
              content: `📎 [${data.file.name}](${data.file.url || `/api/files/${data.file.id}`}) (${formatFileSize(data.file.size)})`,
              ts: data.ts,
            }
            setMessages((prev) => [...prev, fileMsg])
            break
          }

          default:
            break
        }
      } catch {
        // ignore parse errors
      }
    }
  }, [])

  useEffect(() => {
    connectWS()
    return () => {
      if (reconnectTimerRef.current) {
        clearTimeout(reconnectTimerRef.current)
      }
      wsRef.current?.close()
    }
  }, [connectWS])

  // --- Send message ---
  const handleSend = useCallback((content: string) => {
    if (!wsRef.current || wsRef.current.readyState !== WebSocket.OPEN) return

    const userMsg: Message = {
      id: `user-${Date.now()}`,
      type: 'user',
      content,
      ts: Math.floor(Date.now() / 1000),
    }
    setMessages((prev) => [...prev, userMsg])
    setProgress(null)
    setLoading(true)
    setAutoScroll(true)

    const payload: { type: string; content: string; file_ids?: string[] } = {
      type: 'message',
      content,
    }
    if (pendingFiles.length > 0) {
      payload.file_ids = pendingFiles.map((f) => f.id)
      setPendingFiles([])
    }

    wsRef.current.send(JSON.stringify(payload))

    setTimeout(scrollToBottom, 50)
  }, [scrollToBottom, pendingFiles])

  // --- Cancel generation ---
  const handleCancel = useCallback(() => {
    if (!wsRef.current || wsRef.current.readyState !== WebSocket.OPEN) return
    wsRef.current.send(JSON.stringify({ type: 'cancel' }))
    setLoading(false)
    setProgress(null)
  }, [])

  // --- Logout ---
  const handleLogout = async () => {
    if (reconnectTimerRef.current) {
      clearTimeout(reconnectTimerRef.current)
    }
    await fetch('/api/auth/logout', { method: 'POST' })
    wsRef.current?.close()
    onLogout()
  }

  // --- File upload handlers ---
  const handleFileUploaded = useCallback((fileId: string, name: string) => {
    setPendingFiles((prev) => [...prev, { id: fileId, name, size: 0 }])
  }, [])

  const handleFileRemove = useCallback((fileId: string) => {
    setPendingFiles((prev) => prev.filter((f) => f.id !== fileId))
  }, [])

  // --- Drag & drop handlers ---
  const handleDragOver = useCallback((e: React.DragEvent) => {
    e.preventDefault()
    e.stopPropagation()
    setDragActive(true)
  }, [])

  const handleDragLeave = useCallback((e: React.DragEvent) => {
    e.preventDefault()
    e.stopPropagation()
    setDragActive(false)
  }, [])

  const handleDrop = useCallback(async (e: React.DragEvent) => {
    e.preventDefault()
    e.stopPropagation()
    setDragActive(false)

    const files = e.dataTransfer.files
    if (!files || files.length === 0) return

    for (const file of Array.from(files)) {
      const result = await uploadFile(file)
      if (result.ok) {
        handleFileUploaded(result.file_id, result.name)
      } else {
        // Show toast
        const toast = document.createElement('div')
        toast.className = 'file-upload-toast'
        toast.textContent = result.error || '上传失败'
        document.body.appendChild(toast)
        setTimeout(() => {
          toast.classList.add('file-upload-toast-hide')
          setTimeout(() => toast.remove(), 300)
        }, 3000)
      }
    }
  }, [handleFileUploaded])

  // --- Paste handler (for images) ---
  const handlePaste = usePasteUpload(handleFileUploaded, loading)

  return (
    <div className={`flex flex-col h-screen bg-slate-900${dragActive ? ' drag-active' : ''}`}
         onDragOver={handleDragOver}
         onDragLeave={handleDragLeave}
         onDrop={handleDrop}
         onPaste={handlePaste}
    >
      {/* Header */}
      <header className="flex items-center justify-between px-4 py-3 bg-slate-800 border-b border-slate-700">
        <div className="flex items-center gap-3">
          <h1 className="text-lg font-bold text-white">🤖 xbot</h1>
          <span className={`text-xs px-2 py-0.5 rounded-full ${
            connected
              ? 'bg-green-900/50 text-green-400'
              : reconnecting
                ? 'bg-yellow-900/50 text-yellow-400'
                : 'bg-red-900/50 text-red-400'
          }`}>
            {connected ? '● Connected' : reconnecting ? '◐ Reconnecting...' : '○ Disconnected'}
          </span>
        </div>
        <div className="flex items-center gap-2">
          <button
            onClick={() => setSettingsOpen(true)}
            className="text-sm text-slate-400 hover:text-white transition-colors p-1"
            title="设置"
          >
            ⚙️
          </button>
          <button
            onClick={handleLogout}
            className="text-sm text-slate-400 hover:text-white transition-colors"
          >
            Logout
          </button>
        </div>
      </header>

      {/* Disconnected / Reconnecting banner */}
      {!connected && serverStopped.current && (
        <div className="bg-red-900/40 border-b border-red-800/50 px-4 py-2 text-center text-sm text-red-400">
          ⛔ 服务已断开，请刷新页面重新连接
        </div>
      )}
      {reconnecting && !connected && (
        <div className="bg-yellow-900/40 border-b border-yellow-800/50 px-4 py-2 text-center text-sm text-yellow-400">
          ⚠️ 连接断开，正在尝试重连...
        </div>
      )}

      {/* Messages */}
      <div
        ref={messagesContainerRef}
        onScroll={handleContainerScroll}
        className="flex-1 overflow-y-auto px-4 py-4 space-y-4"
      >
        {messages.length === 0 && !loading && (
          <div className="text-center text-slate-500 mt-20">
            <p className="text-2xl mb-2">💬</p>
            <p>开始一段对话</p>
          </div>
        )}

        {messages.map((msg) => (
          <div
            key={msg.id}
            className={`flex ${msg.type === 'user' ? 'justify-end' : 'justify-start'} msg-fade-in`}
          >
            <div
              className={`max-w-[80%] rounded-xl px-4 py-3 ${
                msg.type === 'user'
                  ? 'bg-blue-600 text-white'
                  : 'bg-slate-800 text-slate-200 border border-slate-700'
              }`}
            >
              {msg.type !== 'user' ? (
                <div className="markdown-body">
                  <Markdown components={codeBlockComponents} remarkPlugins={[remarkGfm]}>
                    {msg.content}
                  </Markdown>
                </div>
              ) : (
                <p className="whitespace-pre-wrap">{msg.content}</p>
              )}
              {msg.ts && (
                <div className={`text-xs mt-1 text-right ${
                  msg.type === 'user' ? 'text-blue-200/50' : 'text-slate-500'
                }`}>
                  {formatTime(msg.ts)}
                </div>
              )}
            </div>
          </div>
        ))}

        {/* Progress panel */}
        {(progress || loading) && <ProgressPanel progress={progress} loading={loading} />}

        <div ref={messagesEndRef} />
      </div>

      {/* Scroll to bottom button */}
      {!autoScroll && (messages.length > 0 || loading) && (
        <button
          onClick={() => { scrollToBottom(); setAutoScroll(true) }}
          className="scroll-to-bottom-btn"
        >
          ↓ 新消息
        </button>
      )}

      {/* Input area */}
      <div className="px-4 py-3 bg-slate-800 border-t border-slate-700">
        <div className="flex items-end gap-3 max-w-4xl mx-auto">
          <div className="flex-1">
            {/* Pending files preview */}
            {pendingFiles.length > 0 && (
              <div className="flex flex-wrap gap-2 mb-2">
                {pendingFiles.map((f) => (
                  <div key={f.id} className="file-tag">
                    <span className="file-tag-name">{f.name}</span>
                    <button
                      className="file-tag-remove"
                      onClick={() => handleFileRemove(f.id)}
                      title="移除"
                    >
                      ✕
                    </button>
                  </div>
                ))}
              </div>
            )}
            <TiptapEditor
              onSend={handleSend}
              disabled={loading}
              connected={connected}
            />
          </div>
          <FileUpload
            onUpload={handleFileUploaded}
            disabled={loading}
          />
          {loading && (
            <button
              onClick={handleCancel}
              className="cancel-btn"
              title="停止生成"
            >
              ⏹
            </button>
          )}
        </div>
      </div>

      {/* Settings panel */}
      <SettingsPanel open={settingsOpen} onClose={() => setSettingsOpen(false)} />
    </div>
  )
}
