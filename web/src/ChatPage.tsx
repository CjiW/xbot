import { useEffect, useRef, useState, useCallback } from 'react'
import type { TiptapEditorHandle } from './components/TiptapEditor'
import type { PresetCommand } from './types'
import ProgressPanel from './components/ProgressPanel'
import AssistantTurn from './components/AssistantTurn'

import Markdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import type { WsProgressPayload, IterationSnapshot } from './components/ProgressPanel'


import TiptapEditor from './components/TiptapEditor'
import SettingsPanel from './components/SettingsPanel'
import FileUpload, { uploadFile, usePasteUpload, type PendingFile } from './components/FileUpload'

// --- Lazy rendering: only render when element enters viewport ---
// --- Lazy rendering wrapper: only renders children when element enters viewport ---
function LazyTurn({ children }: { children: React.ReactNode }) {
  const ref = useRef<HTMLDivElement | null>(null)
  const [visible, setVisible] = useState(false)

  useEffect(() => {
    const el = ref.current
    if (!el) return
    const container = el.parentElement
    // Skip IntersectionObserver for small message lists — overhead not worth it
    if ((container?.children.length ?? 0) < 30) {
      // Use microtask to avoid synchronous setState in effect
      const raf = requestAnimationFrame(() => setVisible(true))
      return () => cancelAnimationFrame(raf)
    }
    const observer = new IntersectionObserver(
      ([entry]) => { if (entry.isIntersecting) { setVisible(true); observer.disconnect() } },
      { rootMargin: '300px 0px' }  // pre-render slightly before entering viewport
    )
    observer.observe(el)
    return () => observer.disconnect()
  }, [])

  return (
    <div ref={ref}>
      {visible ? children : <div className="h-6" />}
    </div>
  )
}

interface ChatPageProps {
  onLogout: () => void
}

interface Message {
  id: string
  type: 'user' | 'assistant' | 'system'
  content: string
  ts?: number
  // Saved progress snapshot when this message was finalized (for showing intermediate process)
  savedProgress?: WsProgressPayload | null
  // Full iteration history (persisted across refreshes)
  iterationHistory?: IterationSnapshot[] | null
}

function normalizeIterationHistory(input: unknown): IterationSnapshot[] {
  if (!Array.isArray(input) || input.length === 0) return []

  const toNumber = (v: unknown): number | undefined => (typeof v === 'number' ? v : undefined)

  const normalized: IterationSnapshot[] = []
  for (const raw of input) {
    if (!raw || typeof raw !== 'object') continue
    const snap = raw as Record<string, unknown>

    const iteration = toNumber(snap.iteration ?? snap.Iteration)
    if (iteration == null) continue

    const thinkingRaw = snap.thinking ?? snap.Thinking
    const thinking = typeof thinkingRaw === 'string' ? thinkingRaw : undefined

    const rawTools = Array.isArray(snap.tools) ? snap.tools : (Array.isArray(snap.Tools) ? snap.Tools : [])
    const tools = rawTools
      .filter((t): t is Record<string, unknown> => !!t && typeof t === 'object')
      .map((t) => {
        const name = typeof (t.name ?? t.Name) === 'string' ? String(t.name ?? t.Name) : ''
        const label = typeof (t.label ?? t.Label) === 'string' ? String(t.label ?? t.Label) : undefined
        const status = typeof (t.status ?? t.Status) === 'string' ? String(t.status ?? t.Status) : 'done'

        const elapsedMsLower = toNumber(t.elapsed_ms)
        const elapsedNsLegacy = toNumber(t.Elapsed)
        const elapsedMs = elapsedMsLower ?? (elapsedNsLegacy != null ? Math.round(elapsedNsLegacy / 1_000_000) : undefined)

        return {
          name,
          label,
          status,
          elapsed_ms: elapsedMs,
        }
      })

    normalized.push({
      iteration,
      thinking,
      tools,
    })
  }

  const byIteration = new Map<number, IterationSnapshot>()
  for (const snap of normalized) {
    if (typeof snap?.iteration !== 'number') continue
    byIteration.set(snap.iteration, {
      iteration: snap.iteration,
      thinking: snap.thinking,
      tools: Array.isArray(snap.tools) ? snap.tools : [],
    })
  }

  const sorted = Array.from(byIteration.values()).sort((a, b) => a.iteration - b.iteration)
  const seenThinking = new Set<string>()

  return sorted.map((snap) => {
    const thinking = (snap.thinking || '').trim()
    const dedupedThinking = thinking && !seenThinking.has(thinking) ? snap.thinking : undefined
    if (thinking && !seenThinking.has(thinking)) {
      seenThinking.add(thinking)
    }
    return {
      ...snap,
      thinking: dedupedThinking,
    }
  })
}

function formatTime(ts: number): string {
  return new Date(ts * 1000).toLocaleTimeString('zh-CN', {
    hour: '2-digit',
    minute: '2-digit',
  })
}

// --- Turn-based message grouping (Codex style) ---
type Turn =
  | { type: 'user'; message: Message }
  | { type: 'assistant'; messages: Message[] }

function groupMessagesIntoTurns(messages: Message[]): Turn[] {
  const turns: Turn[] = []
  let currentAssistant: Message[] = []

  for (const msg of messages) {
    if (msg.type === 'user') {
      if (currentAssistant.length > 0) {
        turns.push({ type: 'assistant', messages: currentAssistant })
        currentAssistant = []
      }
      turns.push({ type: 'user', message: msg })
    } else {
      currentAssistant.push(msg)
    }
  }
  if (currentAssistant.length > 0) {
    turns.push({ type: 'assistant', messages: currentAssistant })
  }
  return turns
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
  const [liveIterations, setLiveIterations] = useState<IterationSnapshot[]>([])
  const prevIterationRef = useRef<number>(-1)
  const progressRef = useRef<WsProgressPayload | null>(null) // sync ref to avoid stale closures
  const [autoScroll, setAutoScroll] = useState(true)
  const [reconnecting, setReconnecting] = useState(true) // true = initial connecting state
  const [settingsOpen, setSettingsOpen] = useState(false)
  const [pendingFiles, setPendingFiles] = useState<PendingFile[]>([])
  const [dragActive, setDragActive] = useState(false)
  const [nickname, setNickname] = useState<string>(() => localStorage.getItem('xbot-nickname') || '')
  const editorRef = useRef<TiptapEditorHandle>(null)
  const [presets, setPresets] = useState<PresetCommand[]>([])

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
    fetch('/api/history')
      .then((r) => r.json())
      .then((data) => {
        if (data.ok && data.messages) {
          const hist: Message[] = data.messages
            .filter((m: { role: string; tool_calls?: string; detail?: string }) => {
              if (m.role === 'tool') return false
              // Skip intermediate assistant(tool_calls) messages that have no detail.
              // These were saved for LLM context continuity; the final assistant message's
              // detail field contains the full iteration history that covers these.
              if (m.role === 'assistant' && m.tool_calls && !m.detail) return false
              return true
            })
            .map((m: { role: string; content: string; detail?: string }, i: number) => {
              const msg: Message = {
                id: `hist-${i}`,
                type: m.role === 'user' ? 'user' : m.role === 'assistant' ? 'assistant' : 'system',
                content: m.content,
              }
              // Parse iteration history from detail field
              if (m.detail) {
                try {
                  msg.iterationHistory = normalizeIterationHistory(JSON.parse(m.detail))
                } catch {
                  // ignore parse errors
                }
              }
              return msg
            })
          setMessages(hist)
          // If the last message is from the user, the backend is still processing.
          // Set loading so the progress panel shows up once WS delivers events.
          if (hist.length > 0 && hist[hist.length - 1].type === 'user') {
            setLoading(true)
          }
          setTimeout(scrollToBottom, 100)
        }
      })
      .catch(() => {})
  }, [scrollToBottom])

  // --- WebSocket connection with reconnect ---
  const connectWSRef = useRef<() => void>(() => {})
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
            // Structured progress — accumulate completed iterations, update current
            {
              let p: WsProgressPayload = data.progress
              const prevIter = prevIterationRef.current
              const prevProgress = progressRef.current

              // Guard against same-iteration regressions: some events may carry
              // empty thinking/completed_tools and would otherwise erase visible state.
              if (prevProgress && p.iteration === prevProgress.iteration) {
                const nextThinking = (p.thinking || '').trim()
                const prevThinking = (prevProgress.thinking || '').trim()
                p = {
                  ...p,
                  thinking: nextThinking.length > 0 ? p.thinking : prevProgress.thinking,
                  completed_tools: (p.completed_tools?.length ?? 0) > 0
                    ? p.completed_tools
                    : (prevProgress.completed_tools ?? []),
                }
                if (nextThinking.length === 0 && prevThinking.length > 0) {
                  p.thinking = prevProgress.thinking
                }
              }

              // When iteration advances, snapshot the previous iteration and append
              if (prevIter >= 0 && p.iteration > prevIter && prevProgress) {
                const allTools = [
                  ...(prevProgress.completed_tools ?? []),
                  ...(prevProgress.active_tools ?? []),
                ].map(t => ({
                  name: t.name,
                  label: t.label,
                  status: t.status,
                  elapsed_ms: t.elapsed_ms,
                }))
                setLiveIterations(prev => {
                  const merged = normalizeIterationHistory([
                    ...prev,
                    {
                      iteration: prevIter,
                      thinking: prevProgress.thinking,
                      tools: allTools,
                    },
                  ])
                  return merged
                })
              }

              // Frontend safety net: if backend event for iteration N already carries
              // completed_tools of iteration N-1, persist that snapshot immediately.
              if (p.iteration > 0 && (p.completed_tools?.length ?? 0) > 0) {
                const inferredPrev = p.iteration - 1
                setLiveIterations(prev => {
                  const hasPrev = prev.some((s) => s.iteration === inferredPrev)
                  if (hasPrev) return prev
                  return normalizeIterationHistory([
                    ...prev,
                    {
                      iteration: inferredPrev,
                      tools: (p.completed_tools ?? []).map((t) => ({
                        name: t.name,
                        label: t.label,
                        status: t.status,
                        elapsed_ms: t.elapsed_ms,
                      })),
                    },
                  ])
                })
              }

              prevIterationRef.current = p.iteration
              progressRef.current = p
              setProgress(p)
            }
            setLoading(true)
            break

          case 'text':
          case 'card': {
            // Final message — snapshot current iteration + all completed iterations
            const progressSnap = progressRef.current
              ? {
                  ...progressRef.current,
                  active_tools: [],
                } as WsProgressPayload
              : null

            // Build current iteration snapshot
            const currentSnap = progressSnap ? (() => {
              const allTools = [
                ...(progressSnap.completed_tools ?? []),
              ].map(t => ({
                name: t.name,
                label: t.label,
                status: t.status,
                elapsed_ms: t.elapsed_ms,
              }))
              return {
                iteration: prevIterationRef.current,
                thinking: progressSnap.thinking,
                tools: allTools,
              }
            })() : null

            // Combine local snapshots first.
            let localHistory: IterationSnapshot[] = []
            setLiveIterations(prev => {
              localHistory = [...prev]
              if (currentSnap) localHistory.push(currentSnap)
              return []
            })

            localHistory = normalizeIterationHistory(localHistory)

            // Prefer backend-provided history so current view matches refreshed history.
            let finalHistory = localHistory
            if (data.progress_history) {
              try {
                const serverHistory = normalizeIterationHistory(JSON.parse(data.progress_history))
                if (serverHistory.length > 0) {
                  finalHistory = serverHistory
                }
              } catch {
                // keep local snapshots
              }
            }

            setProgress(null)
            prevIterationRef.current = -1
            progressRef.current = null
            setLoading(false)

            const msg: Message = {
              id: data.id || `ws-${Date.now()}`,
              type: data.type === 'card' ? 'system' : 'assistant',
              content: data.content,
              ts: data.ts,
              savedProgress: progressSnap,
              iterationHistory: finalHistory.length > 0 ? finalHistory : undefined,
            }
            setMessages((prev) => [...prev, msg])
            break
          }

          case 'file': {
            setProgress(null)
            setLiveIterations([])
            prevIterationRef.current = -1
            progressRef.current = null
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
    setLiveIterations([])
    prevIterationRef.current = -1
    progressRef.current = null
    setLoading(true)
    setAutoScroll(true)

    const payload: { type: string; content: string; file_ids?: string[]; file_names?: string[] } = {
      type: 'message',
      content,
    }
    if (pendingFiles.length > 0) {
      payload.file_ids = pendingFiles.map((f) => f.id)
      payload.file_names = pendingFiles.map((f) => f.name)
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
    setLiveIterations([])
    prevIterationRef.current = -1
  }, [])

  // --- Preset commands ---
  useEffect(() => {
    fetch('/api/settings')
      .then(r => r.json())
      .then(data => {
        if (data.ok && data.settings?.preset_commands) {
          try {
            const parsed = JSON.parse(data.settings.preset_commands)
            if (Array.isArray(parsed)) setPresets(parsed)
          } catch { /* ignore */ }
        }
      })
      .catch(() => {})
  }, [])

  const handlePresetClick = useCallback((preset: PresetCommand) => {
    if (preset.fill) {
      editorRef.current?.setContent(preset.content)
    } else {
      handleSend(preset.content)
    }
  }, [handleSend])

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
          <h1 className="text-lg font-bold text-white">🤖 xbot{nickname ? ` · ${nickname}` : ''}</h1>
          <span className={`text-xs px-2 py-0.5 rounded-full ${
            connected
              ? 'bg-green-900/50 text-green-400'
              : reconnecting
                ? 'bg-yellow-900/50 text-yellow-400'
                : 'bg-red-900/50 text-red-400'
          }`}>
            {connected ? '● Connected' : reconnecting ? '◐ Connecting...' : '○ Disconnected'}
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
        className="flex-1 overflow-y-auto px-4 py-4 space-y-4 chat-messages"
      >
        {messages.length === 0 && !loading && (
          <div className="text-center text-slate-500 mt-20">
            <p className="text-2xl mb-2">💬</p>
            <p>开始一段对话</p>
          </div>
        )}

        {(() => {
          const turns = groupMessagesIntoTurns(messages)
          return turns.map((turn, i) => {
            const isLatestTurn = i === turns.length - 1
            // Only bind live progress to the latest assistant turn if we are
            // actively processing a request. After a page refresh the last
            // historical assistant turn would otherwise steal the progress.
            const isActive = loading || progress !== null
            if (turn.type === 'user') {
	              const content = (
	                <div className="flex justify-end msg-fade-in">
	                  <div className="max-w-[80%] rounded-xl px-4 py-3 bg-blue-600 text-white markdown-body text-sm">
	                    <Markdown remarkPlugins={[remarkGfm]}>{turn.message.content}</Markdown>
	                    {turn.message.ts && (
	                      <div className="text-xs mt-1 text-right text-blue-200/50">
	                        {formatTime(turn.message.ts)}
	                      </div>
	                    )}
	                  </div>
	                </div>
	              )

              return isLatestTurn ? (
                <div key={turn.message.id}>{content}</div>
              ) : (
                <LazyTurn key={turn.message.id}>{content}</LazyTurn>
              )
            }
            // Assistant turn — last one gets live progress & loading
            const turnSavedProgress = turn.messages[turn.messages.length - 1]?.savedProgress ?? null
            const assistantContent = (
              <AssistantTurn
                key={turn.messages[0].id}
                messages={turn.messages}
                progress={isLatestTurn && isActive ? progress : null}
                liveIterations={isLatestTurn && isActive ? liveIterations : undefined}
                loading={isLatestTurn && isActive && loading}
                savedProgress={turnSavedProgress}
              />
            )
            return isLatestTurn ? assistantContent : (
              <LazyTurn key={turn.messages[0].id}>{assistantContent}</LazyTurn>
            )
          })
        })()}

        {/* Standalone progress when no assistant turn exists yet (e.g. right after user sends a message) */}
        {messages.length > 0 && messages[messages.length - 1].type === 'user' && (progress || loading) && (
          <ProgressPanel progress={progress} liveIterations={liveIterations} loading={loading} />
        )}

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

      {/* Preset commands bar */}
      {presets.length > 0 && (
        <div className="preset-bar">
          {[...presets]
            .sort((a, b) => (a.sort ?? 0) - (b.sort ?? 0))
            .map((p) => (
              <button
                key={p.id}
                className="preset-chip"
                onClick={() => handlePresetClick(p)}
                disabled={loading || !connected}
                title={p.content.length > 50 ? p.content.slice(0, 50) + '...' : p.content}
              >
                {p.icon || '⚡'} {p.label}
              </button>
            ))}
        </div>
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
              ref={editorRef}
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
      <SettingsPanel
        open={settingsOpen}
        onClose={() => setSettingsOpen(false)}
        onNicknameChange={(n) => setNickname(n)}
        onPresetsChange={setPresets}
      />
    </div>
  )
}
