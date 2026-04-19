import { useEffect, useRef, useState, useCallback } from 'react'
import type { TiptapEditorHandle } from './components/TiptapEditor'
import type { PresetCommand } from './types'
import ProgressPanel from './components/ProgressPanel'
import AssistantTurn from './components/AssistantTurn'

import Markdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import type { WsProgressPayload, IterationSnapshot } from './components/ProgressPanel'
import { getCodeBlockProps } from './components/CodeBlock'

const codeBlockComponents = getCodeBlockProps()


import TiptapEditor from './components/TiptapEditor'
import SettingsPanel from './components/SettingsPanel'
import FileUpload, { uploadFile, usePasteUpload, type PendingFile } from './components/FileUpload'

// --- Lazy rendering: only render when element enters viewport ---
// Use `eager` to skip IntersectionObserver (for turns near bottom that need instant render).
function LazyTurn({ children, eager }: { children: React.ReactNode; eager?: boolean }) {
  const ref = useRef<HTMLDivElement | null>(null)
  const [visible, setVisible] = useState(() => eager ?? false)

  useEffect(() => {
    if (eager) return
    const el = ref.current
    if (!el) return
    const container = el.parentElement
    // Skip IntersectionObserver for small message lists — overhead not worth it
    if ((container?.children.length ?? 0) < 30) {
      const raf = requestAnimationFrame(() => setVisible(true))
      return () => cancelAnimationFrame(raf)
    }
    const observer = new IntersectionObserver(
      ([entry]) => { if (entry.isIntersecting) { setVisible(true); observer.disconnect() } },
      { rootMargin: '300px 0px' }  // pre-render slightly before entering viewport
    )
    observer.observe(el)
    return () => observer.disconnect()
  }, [eager])

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

    const reasoningRaw = snap.reasoning ?? snap.Reasoning
    const reasoning = typeof reasoningRaw === 'string' ? reasoningRaw : undefined

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
      reasoning,
      tools,
    })
  }

  const byIteration = new Map<number, IterationSnapshot>()
  for (const snap of normalized) {
    if (typeof snap?.iteration !== 'number') continue
    byIteration.set(snap.iteration, {
      iteration: snap.iteration,
      thinking: snap.thinking,
      reasoning: snap.reasoning,
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

// --- Attachment parsing & rendering ---

interface ParsedAttachment {
  type: 'image' | 'file'
  name: string
  url?: string
  size?: number
  raw: string
}

const reAttachment = /<(image|file)\s+([^>]*?)\/?>/gi

function parseAttachments(content: string): { attachments: ParsedAttachment[]; cleanContent: string } {
  const attachments: ParsedAttachment[] = []
  let cleanContent = content

  // Remove duplicate markdown image syntax that follows <image> tags
  // (backend sends both XML and ![name](url))
  cleanContent = cleanContent.replace(/(<image\s[^>]*?\/?>)\s*\n?!?\[[^\]]*\]\([^)]+\)/gi, '$1')

  cleanContent = cleanContent.replace(reAttachment, (match, type, attrs) => {
    const nameMatch = attrs.match(/(?:name|filename)="([^"]*)"/)
    const urlMatch = attrs.match(/url="([^"]*)"/)
    const sizeMatch = attrs.match(/size="(\d+)"/)
    const name = nameMatch?.[1] || (type === 'image' ? '图片' : '文件')
    const url = urlMatch?.[1]
    const size = sizeMatch ? parseInt(sizeMatch[1], 10) : undefined
    const idx = attachments.length
    attachments.push({ type: type as 'image' | 'file', name, url, size, raw: match })
    return `{{attachment-${idx}}}`
  })

  // Clean up empty lines left by removed tags
  cleanContent = cleanContent.replace(/\n{2,}/g, '\n\n').trim()

  return { attachments, cleanContent }
}

function AttachmentCard({ attachment }: { attachment: ParsedAttachment }) {
  if (attachment.type === 'image' && attachment.url) {
    return (
      <div className="attachment-card attachment-image">
        <img
          src={attachment.url}
          alt={attachment.name}
          className="attachment-img"
          loading="lazy"
          onClick={() => window.open(attachment.url, '_blank')}
        />
        <div className="attachment-meta">
          <span className="truncate">{attachment.name}</span>
          {attachment.size != null && <span>{formatFileSize(attachment.size)}</span>}
        </div>
      </div>
    )
  }

  return (
    <a
      href={attachment.url || '#'}
      target="_blank"
      rel="noopener noreferrer"
      className="attachment-card attachment-file"
    >
      <div className="attachment-file-icon">
        {attachment.name.match(/\.(pdf)$/i) ? '📄' :
         attachment.name.match(/\.(doc|docx)$/i) ? '📝' :
         attachment.name.match(/\.(xls|xlsx|csv)$/i) ? '📊' :
         attachment.name.match(/\.(zip|tar|gz|rar|7z)$/i) ? '📦' :
         attachment.name.match(/\.(mp4|avi|mov|mkv)$/i) ? '🎬' :
         attachment.name.match(/\.(mp3|wav|flac)$/i) ? '🎵' :
         '📎'}
      </div>
      <div className="attachment-file-info">
        <span className="truncate">{attachment.name}</span>
        {attachment.size != null && <span>{formatFileSize(attachment.size)}</span>}
      </div>
    </a>
  )
}

function UserMessageContent({ content }: { content: string }) {
  const { attachments, cleanContent } = parseAttachments(content)

  // If no attachments found, render as normal markdown
  if (attachments.length === 0) {
    return <Markdown remarkPlugins={[remarkGfm]} components={codeBlockComponents}>{content}</Markdown>
  }

  // Split clean content by attachment placeholders and render interleaved
  const parts = cleanContent.split(/(\{\{attachment-\d+\}\})/)
  const elements: React.ReactNode[] = []

  for (const part of parts) {
    const match = part.match(/^\{\{attachment-(\d+)\}\}$/)
    if (match) {
      const idx = parseInt(match[1], 10)
      if (idx < attachments.length) {
        elements.push(<AttachmentCard key={`att-${idx}`} attachment={attachments[idx]} />)
      }
    } else if (part.trim()) {
      elements.push(
        <Markdown key={`md-${elements.length}`} remarkPlugins={[remarkGfm]} components={codeBlockComponents}>
          {part}
        </Markdown>
      )
    }
  }

  return <>{elements}</>
}

export default function ChatPage({ onLogout }: ChatPageProps) {
  const [messages, setMessages] = useState<Message[]>([])
  const [connected, setConnected] = useState(false)
  const [loading, setLoading] = useState(false)
  const [progress, setProgress] = useState<WsProgressPayload | null>(null)
  const [liveIterations, _setLiveIterations] = useState<IterationSnapshot[]>([])
  const liveIterationsRef = useRef<IterationSnapshot[]>([])
  // Keep ref in sync so we can read the latest value synchronously
  // (React setState updater callbacks are async and cannot be relied upon).
  const setLiveIterationsSync = (updater: IterationSnapshot[] | ((prev: IterationSnapshot[]) => IterationSnapshot[])) => {
    _setLiveIterations(prev => {
      const next = typeof updater === 'function' ? updater(prev) : updater
      liveIterationsRef.current = next
      return next
    })
  }
  const prevIterationRef = useRef<number>(-1)
  const progressRef = useRef<WsProgressPayload | null>(null) // sync ref to avoid stale closures
  const reasoningRef = useRef<string>('') // accumulated reasoning from stream_content
  const streamingContentRef = useRef<string>('') // accumulated content from stream_content
  const lastSeqRef = useRef<number>(0) // last processed event seq (for dedup & sync)
  const [autoScroll, setAutoScroll] = useState(true)
  const [reconnecting, setReconnecting] = useState(true) // true = initial connecting state
  const [settingsOpen, setSettingsOpen] = useState(false)
  const [pendingFiles, setPendingFiles] = useState<PendingFile[]>([])
  const [dragActive, setDragActive] = useState(false)
  const [nickname, setNickname] = useState<string>(() => localStorage.getItem('xbot-nickname') || '')
  const editorRef = useRef<TiptapEditorHandle>(null)
  const [presets, setPresets] = useState<PresetCommand[]>([])
  const [askUser, setAskUser] = useState<{ questions: { question: string; options?: string[] }[]; answers: Record<string, string>; currentQ: number } | null>(null)
  const [toasts, setToasts] = useState<{ id: number; message: string; type: 'info' | 'error' | 'success' }[]>([])
  const [currentModel, setCurrentModel] = useState('')
  const [availableModels, setAvailableModels] = useState<string[]>([])
  const [modelDropdownOpen, setModelDropdownOpen] = useState(false)
  const [searchOpen, setSearchOpen] = useState(false)
  const [searchQuery, setSearchQuery] = useState('')
  const [searchResults, setSearchResults] = useState<Array<{ id: number; role: string; snippet: string; created_at: string }>>([])
  const [searchLoading, setSearchLoading] = useState(false)
  const searchInputRef = useRef<HTMLInputElement>(null)
  const askUserInputRef = useRef<HTMLInputElement>(null)
  const showToast = useCallback((message: string, type: 'info' | 'error' | 'success' = 'info') => {
    const id = Date.now()
    setToasts(prev => [...prev, { id, message, type }])
    setTimeout(() => setToasts(prev => prev.filter(t => t.id !== id)), 3000)
  }, [])

  const handleModelSwitch = useCallback(async (model: string) => {
    setModelDropdownOpen(false)
    if (model === currentModel) return
    try {
      const resp = await fetch('/api/llm-config/model', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ model }),
      })
      const data = await resp.json()
      if (data.ok) {
        setCurrentModel(model)
        showToast(`已切换到 ${model}`, 'success')
      } else {
        showToast(data.error || '切换失败', 'error')
      }
    } catch {
      showToast('切换失败', 'error')
    }
  }, [currentModel, showToast])

  // --- Load available models on mount ---
  useEffect(() => {
    fetch('/api/llm-config')
      .then(r => r.json())
      .then(data => {
        if (data.ok) {
          setCurrentModel(data.model || '')
          setAvailableModels(data.models || [])
        }
      })
      .catch(() => {})
  }, [])

  // --- Search: debounce 300ms ---
  useEffect(() => {
    if (!searchOpen || !searchQuery.trim()) {
      setSearchResults([])
      return
    }
    const controller = new AbortController()
    const timer = setTimeout(async () => {
      setSearchLoading(true)
      try {
        const resp = await fetch(`/api/search?q=${encodeURIComponent(searchQuery.trim())}&limit=20`, {
          signal: controller.signal,
        })
        const data = await resp.json()
        if (data.ok) {
          setSearchResults(data.results || [])
        }
      } catch (e) {
        if (e instanceof DOMException && e.name === 'AbortError') return
      }
      setSearchLoading(false)
    }, 300)
    return () => {
      clearTimeout(timer)
      controller.abort()
    }
  }, [searchQuery, searchOpen])

  // --- Search: Ctrl+K shortcut ---
  useEffect(() => {
    const handler = (e: KeyboardEvent) => {
      if ((e.ctrlKey || e.metaKey) && e.key === 'k') {
        e.preventDefault()
        setSearchOpen(prev => {
          const next = !prev
          if (next) {
            setTimeout(() => searchInputRef.current?.focus(), 0)
          }
          return next
        })
        if (!searchOpen) {
          setSearchQuery('')
          setSearchResults([])
        }
      }
    }
    window.addEventListener('keydown', handler)
    return () => window.removeEventListener('keydown', handler)
  }, [searchOpen])

  const wsRef = useRef<WebSocket | null>(null)
  const messagesContainerRef = useRef<HTMLDivElement>(null)
  const messagesEndRef = useRef<HTMLDivElement>(null)
  const reconnectDelayRef = useRef(1000)
  const reconnectTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const serverStopped = useRef(false)
  const intentionalClose = useRef(false)


  // --- Scroll management ---
  const isNearBottom = useCallback(() => {
    const el = messagesContainerRef.current
    if (!el) return true
    return el.scrollHeight - el.scrollTop - el.clientHeight <= 150
  }, [])

  const scrollToBottom = useCallback((behavior: ScrollBehavior = 'instant') => {
    const el = messagesContainerRef.current
    if (!el) return
    el.scrollTo({ top: el.scrollHeight, behavior })
  }, [])

  const handleContainerScroll = useCallback(() => {
    setAutoScroll(isNearBottom())
  }, [isNearBottom])

  // Auto-scroll during streaming/progress updates — instant, no animation.
  // Only follows when user is already at the bottom (autoScroll=true).
  useEffect(() => {
    if (autoScroll) {
      scrollToBottom('instant')
    }
  }, [messages, progress, autoScroll, scrollToBottom])

  // --- Load history on mount ---
  useEffect(() => {
    fetch('/api/history')
      .then((r) => r.json())
      .then((data) => {
        if (data.ok && data.messages) {
          const hist: Message[] = data.messages
            .filter((m: { role: string; content?: string; tool_calls?: string; detail?: string; display_only?: number }) => {
              if (m.role === 'tool') return false
              // Skip intermediate assistant(tool_calls) messages that have no detail.
              // These were saved for LLM context continuity; the final assistant message's
              // detail field contains the full iteration history that covers these.
              if (m.role === 'assistant' && m.tool_calls && !m.detail) return false
              // Skip display-only assistant messages without content (cancelled placeholders)
              // when there's no iteration history to show.
              if (m.role === 'assistant' && m.display_only && !m.content && !m.detail) return false
              return true
            })
            .map((m: { id: number; role: string; content: string; detail?: string; created_at?: string }) => {
              const msg: Message = {
                id: `hist-${m.id}`,
                type: m.role === 'user' ? 'user' : m.role === 'assistant' ? 'assistant' : 'system',
                content: m.content,
                ts: m.created_at ? Math.floor(new Date(m.created_at).getTime() / 1000) : undefined,
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
          // If the backend reports it's actively processing, set loading.
          // Also check if the last message is from the user (legacy detection).
          const isProcessing = data.processing === true
          const lastIsUser = hist.length > 0 && hist[hist.length - 1].type === 'user'
          if (isProcessing && lastIsUser) {
            setLoading(true)
          }
          // Restore active progress from server (mid-session refresh recovery).
          // This allows the frontend to immediately show iteration state and
          // streaming content without waiting for WS reconnect.
          if (isProcessing && data.active_progress) {
            const ap = data.active_progress
            progressRef.current = {
              phase: ap.phase || 'running',
              iteration: ap.iteration || 0,
              thinking: ap.thinking || '',
              active_tools: (ap.active_tools || []).map((t: { name: string; label: string; status: string; summary: string }) => ({
                name: t.name, label: t.label, status: t.status, summary: t.summary,
              })),
              completed_tools: (ap.completed_tools || []).map((t: { name: string; label: string; status: string; summary: string }) => ({
                name: t.name, label: t.label, status: t.status, summary: t.summary,
              })),
            }
            prevIterationRef.current = ap.iteration || 0
            if (ap.thinking) {
              reasoningRef.current = ap.thinking
            }
            setProgress(progressRef.current)
            // If there's stream content, create/update a streaming message
            if (ap.stream_content) {
              streamingContentRef.current = ap.stream_content
              setMessages(prev => [...prev, {
                id: '__streaming__',
                type: 'assistant' as const,
                content: ap.stream_content,
              }])
            }
            // Restore iteration history (completed iterations 1..N-1)
            if (ap.iteration_history && ap.iteration_history.length > 0) {
              const restoredIterations: IterationSnapshot[] = ap.iteration_history.map(
	                (iter: { iteration: number; thinking?: string; reasoning?: string; completed_tools?: { name: string; label?: string; status: string; summary?: string }[] }) => ({
	                  iteration: iter.iteration,
	                  thinking: iter.thinking || '',
	                  reasoning: iter.reasoning || '',
	                  tools: (iter.completed_tools || []).map(t => ({
	                    name: t.name,
	                    label: t.label,
	                    status: t.status,
	                    summary: t.summary,
	                  })),
	                })
	              )
              setLiveIterationsSync(restoredIterations)
            }
          }
          // Store last_seq for WS sync handshake
          if (data.last_seq) {
            lastSeqRef.current = data.last_seq
          }
          // Scroll to bottom after initial history load.
          // Two-phase: first instant scroll after DOM settles, then a re-scroll
          // after one frame to catch any lazy-loaded content that expanded.
          setTimeout(() => {
            scrollToBottom('instant')
            requestAnimationFrame(() => scrollToBottom('instant'))
          }, 100)
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
      intentionalClose.current = false
      reconnectDelayRef.current = 1000
      // Send sync handshake with last_seq from history API.
      // Server replays missed events (covers GAP between history load and WS connect).
      ws.send(JSON.stringify({ type: 'sync', last_seq: lastSeqRef.current }))
      if (reconnectTimerRef.current) {
        clearTimeout(reconnectTimerRef.current)
        reconnectTimerRef.current = null
      }
    }

    ws.onerror = (e) => {
      console.warn('[WS] error', e)
    }

    ws.onclose = (e) => {
      setConnected(false)

      // Normal closure (1000) or going away (1001) = server shutdown, don't reconnect
      // Skip if this is an intentional close (logout / component unmount)
      if (e.code === 1000 || e.code === 1001) {
        if (!intentionalClose.current) {
          serverStopped.current = true
        }
        setReconnecting(false)
        return
      }

      setReconnecting(true)

      // Exponential backoff reconnect with jitter
      if (reconnectTimerRef.current) {
        clearTimeout(reconnectTimerRef.current)
      }
      const jitter = Math.random() * 0.5 + 0.5 // 0.5x - 1.0x random factor
      const delay = Math.round(reconnectDelayRef.current * jitter)
      reconnectTimerRef.current = setTimeout(() => {
        connectWS()
      }, delay)
      reconnectDelayRef.current = Math.min(reconnectDelayRef.current * 2, 30000)
    }

    ws.onmessage = (e) => {
      try {
        const data = JSON.parse(e.data)

        // Seq-based dedup: ignore events we've already processed.
        // Events from replay (sync) or duplicate pushes are safely skipped.
        if (data.seq && data.seq <= lastSeqRef.current) {
          return
        }
        if (data.seq) {
          lastSeqRef.current = data.seq
        }

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

              // When iteration advances, snapshot the previous iteration and append.
              // Prefer reasoningRef over prevProgress.thinking — progress_structured
              // may have overwritten thinking with empty string.
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
                const snapThinking = reasoningRef.current.trim() || prevProgress.thinking || ''
                setLiveIterationsSync(prev => {
                  const merged = normalizeIterationHistory([
                    ...prev,
                    {
                      iteration: prevIter,
                      thinking: snapThinking,
                      tools: allTools,
                    },
                  ])
                  return merged
                })
                // Clear reasoning for the new iteration
                reasoningRef.current = ''
              }

              // Frontend safety net: if backend event for iteration N already carries
              // completed_tools of iteration N-1, persist that snapshot immediately.
              if (p.iteration > 0 && (p.completed_tools?.length ?? 0) > 0) {
                const inferredPrev = p.iteration - 1
                setLiveIterationsSync(prev => {
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

          case 'stream_content': {
            const reasoning = data.progress?.reasoning_stream_content || ''
            const content = data.progress?.stream_content || ''
            if (!reasoning && !content) break

            // Accumulate reasoning
            // NOTE: backend sends accumulated full text, so we REPLACE not append
            if (reasoning) {
              reasoningRef.current = reasoning
              if (progressRef.current) {
                progressRef.current = { ...progressRef.current, thinking: reasoningRef.current }
                setProgress({ ...progressRef.current })
              } else {
                const p: WsProgressPayload = {
                  phase: 'thinking',
                  iteration: prevIterationRef.current >= 0 ? prevIterationRef.current : 0,
                  thinking: reasoningRef.current,
                  active_tools: [],
                  completed_tools: [],
                }
                progressRef.current = p
                prevIterationRef.current = p.iteration
                setProgress(p)
              }
            }

            // Accumulate content — show as streaming text in the assistant turn
            // NOTE: backend sends accumulated full text (not delta), so we REPLACE not append
            if (content) {
              streamingContentRef.current = content
              setMessages(prev => {
                // Find or create a streaming placeholder message at the end
                const last = prev[prev.length - 1]
                if (last && last.id === '__streaming__') {
                  return [...prev.slice(0, -1), { ...last, content: content }]
                }
                return [...prev, {
                  id: '__streaming__',
                  type: 'assistant' as const,
                  content: content,
                }]
              })
            }

            setLoading(true)
            break
          }

          case 'text':
          case 'card': {
            // Final message — snapshot current iteration + all completed iterations
            const accumulatedReasoning = reasoningRef.current.trim()
            const progressSnap = progressRef.current
              ? {
                  ...progressRef.current,
                  thinking: progressRef.current.thinking || accumulatedReasoning,
                  active_tools: [],
                } as WsProgressPayload
              : accumulatedReasoning
                ? ({
                    phase: 'done' as const,
                    iteration: prevIterationRef.current >= 0 ? prevIterationRef.current : 0,
                    thinking: accumulatedReasoning,
                    active_tools: [],
                    completed_tools: [],
                  } as WsProgressPayload)
                : null

            // Build current iteration snapshot — prefer reasoningRef over progress thinking
            // to avoid losing reasoning that progress_structured may have overwritten
            const snapThinking = accumulatedReasoning || progressSnap?.thinking || ''
            const currentSnap = progressSnap ? (() => {
              const allTools = [
                ...(progressSnap.completed_tools ?? []),
              ].map(t => ({
                name: t.name,
                label: t.label,
                status: t.status,
                elapsed_ms: t.elapsed_ms,
                summary: t.summary,
              }))
              return {
                iteration: prevIterationRef.current,
                thinking: snapThinking,
                tools: allTools,
              }
            })() : null

            const currentLive = liveIterationsRef.current ?? []
            let localHistory: IterationSnapshot[] = [...currentLive]
            if (currentSnap) localHistory.push(currentSnap)
            localHistory = normalizeIterationHistory(localHistory)

            setLiveIterationsSync([])

            localHistory = normalizeIterationHistory(localHistory)

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
            reasoningRef.current = ''
            lastSeqRef.current = 0
            setLoading(false)

            // Use accumulated streaming content if available, otherwise use server content
            const finalContent = streamingContentRef.current || data.content || ''
            streamingContentRef.current = ''

            // Replace streaming placeholder with final message
            setMessages((prev) => {
              const filtered = prev.filter(m => m.id !== '__streaming__')
              const msg: Message = {
                id: data.id || `ws-${Date.now()}`,
                type: data.type === 'card' ? 'system' : 'assistant',
                content: finalContent,
                ts: data.ts,
                savedProgress: progressSnap,
                iterationHistory: finalHistory.length > 0 ? finalHistory : undefined,
              }
              return [...filtered, msg]
            })
            break
          }

          case 'user_echo': {
            // Update optimistic user message with complete content (including file info)
            if (data.original_content) {
              setMessages((prev) => prev.map((m) =>
                m.type === 'user' && m.content === data.original_content
                  ? { ...m, content: data.content }
                  : m
              ))
            }
            break
          }

          case 'ask_user': {
            const questions = data.progress?.questions || []
            if (questions.length > 0) {
              setAskUser({ questions, answers: {}, currentQ: 0 })
            }
            break
          }

          case 'runner_status': {
            try {
              const detail = data.content ? JSON.parse(data.content) : {}
              window.dispatchEvent(new CustomEvent('runner-status-change', {
                detail: { runnerName: detail.runner_name, online: detail.online },
              }))
            } catch { /* ignore */ }
            break
          }

          case 'sync_progress': {
            try {
              const detail = data.content ? JSON.parse(data.content) : {}
              if (detail.message) showToast(detail.message, detail.phase === 'done' ? 'success' : 'info')
            } catch { /* ignore */ }
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
      intentionalClose.current = true
      wsRef.current?.close()
    }
  }, [connectWS])

  // --- Send message ---
  const handleSend = useCallback((content: string) => {
    if (!wsRef.current || wsRef.current.readyState !== WebSocket.OPEN) return

    // Slash commands
    const trimmed = content.trim()
    if (trimmed.startsWith('/')) {
      const cmd = trimmed.toLowerCase()
      if (cmd === '/clear') {
        // Clear both frontend state and backend history
        setMessages([])
        setProgress(null)
        setLiveIterationsSync([])
        prevIterationRef.current = -1
        progressRef.current = null
        reasoningRef.current = ''
        streamingContentRef.current = ''
        setLoading(false)
        fetch('/api/history', { method: 'DELETE' }).catch(() => {})
        showToast('对话已清空', 'info')
        return
      }
      if (cmd === '/new') {
        setMessages([])
        setProgress(null)
        setLiveIterationsSync([])
        prevIterationRef.current = -1
        progressRef.current = null
        reasoningRef.current = ''
        streamingContentRef.current = ''
        setLoading(false)
        showToast('新对话', 'info')
        return
      }
      if (cmd === '/help') {
        const helpMsg: Message = {
          id: `help-${Date.now()}`,
          type: 'system',
          content: `## 可用命令\n\n| 命令 | 说明 |\n|------|------|\n| /clear | 清空对话 |\n| /new | 新对话 |\n| /compact | 压缩上下文 |\n| /help | 显示帮助 |\n| /cancel | 取消当前生成 |`,
          ts: Math.floor(Date.now() / 1000),
        }
        setMessages(prev => [...prev, helpMsg])
        return
      }
      // For /compact, /cancel, /model, /models — send as normal message (backend handles them)
    }

    const userMsg: Message = {
      id: `user-${Date.now()}`,
      type: 'user',
      content,
      ts: Math.floor(Date.now() / 1000),
    }
    setMessages((prev) => [...prev, userMsg])
    setProgress(null)
    setLiveIterationsSync([])
    prevIterationRef.current = -1
    progressRef.current = null
    reasoningRef.current = ''
    streamingContentRef.current = ''
    setLoading(true)
    setAutoScroll(true)

    const payload: { type: string; content: string; file_ids?: string[]; file_names?: string[]; file_sizes?: number[]; upload_keys?: string[]; file_mimes?: string[] } = {
      type: 'message',
      content,
    }
    if (pendingFiles.length > 0) {
      // Separate local files from OSS files
      const localFiles = pendingFiles.filter((f) => !f.isOSS)
      const ossFiles = pendingFiles.filter((f) => f.isOSS)

      if (localFiles.length > 0) {
        payload.file_ids = localFiles.map((f) => f.id)
        payload.file_names = localFiles.map((f) => f.name)
      }
      if (ossFiles.length > 0) {
        payload.upload_keys = ossFiles.map((f) => f.uploadKey!)
        payload.file_names = [...(payload.file_names || []), ...ossFiles.map((f) => f.name)]
        payload.file_sizes = [...(payload.file_sizes || []), ...ossFiles.map((f) => f.size)]
        payload.file_mimes = [...(payload.file_mimes || []), ...ossFiles.map((f) => f.mime || '')]
      }
      setPendingFiles([])
    }

    wsRef.current.send(JSON.stringify(payload))

    setTimeout(() => scrollToBottom(isNearBottom() ? 'instant' : 'smooth'), 50)
  }, [scrollToBottom, isNearBottom, pendingFiles])

  // --- Cancel generation ---
  const handleCancel = useCallback(() => {
    if (!wsRef.current || wsRef.current.readyState !== WebSocket.OPEN) return
    wsRef.current.send(JSON.stringify({ type: 'cancel' }))
    setLoading(false)
    setProgress(null)
    setLiveIterationsSync([])
    prevIterationRef.current = -1
    progressRef.current = null
    reasoningRef.current = ''
    streamingContentRef.current = ''
    // Remove streaming placeholder if present
    setMessages(prev => prev.filter(m => m.id !== '__streaming__'))
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
    intentionalClose.current = true
    await fetch('/api/auth/logout', { method: 'POST' })
    wsRef.current?.close()
    onLogout()
  }

  // --- File upload handlers ---
  const handleFileUploaded = useCallback((file: PendingFile) => {
    setPendingFiles((prev) => [...prev, file])
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
        handleFileUploaded({ id: result.id, name: result.name, size: result.size, mime: result.mime, uploadKey: result.uploadKey, isOSS: result.isOSS })
      } else {
        showToast(result.error || '上传失败', 'error')
      }
    }
  }, [handleFileUploaded, showToast])

  const submitAnswer = useCallback((value: string) => {
    if (!value.trim()) return
    const newAnswers = { ...askUser!.answers, [askUser!.currentQ]: value.trim() }
    if (askUser!.currentQ < askUser!.questions.length - 1) {
      setAskUser({ ...askUser!, answers: newAnswers, currentQ: askUser!.currentQ + 1 })
      // Focus input after advancing to next question
      setTimeout(() => askUserInputRef.current?.focus(), 0)
    } else {
      if (wsRef.current?.readyState === WebSocket.OPEN) {
        wsRef.current.send(JSON.stringify({ type: 'ask_user_response', answers: newAnswers, cancelled: false }))
      } else {
        showToast('连接已断开，请刷新页面', 'error')
      }
      setAskUser(null)
    }
  }, [askUser, showToast])

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
          {/* Model selector */}
          {availableModels.length > 0 && (
            <div className="relative">
              <button
                onClick={() => setModelDropdownOpen(!modelDropdownOpen)}
                className="text-xs px-2 py-0.5 rounded-full bg-slate-700/50 text-slate-300 hover:bg-slate-700 hover:text-white transition-colors flex items-center gap-1"
                title="切换模型"
              >
                🧠 {currentModel || 'default'}
                <span className="text-[10px]">▾</span>
              </button>
              {modelDropdownOpen && (
                <>
                  <div className="fixed inset-0 z-40" onClick={() => setModelDropdownOpen(false)} />
                  <div className="absolute top-full left-0 mt-1 bg-slate-800 border border-slate-600 rounded-lg shadow-xl z-50 py-1 min-w-[200px] max-h-64 overflow-y-auto">
                    {availableModels.map(model => (
                      <button
                        key={model}
                        onClick={() => handleModelSwitch(model)}
                        className={`w-full text-left px-3 py-2 text-sm hover:bg-slate-700 transition-colors ${
                          model === currentModel ? 'text-blue-400 bg-blue-500/10' : 'text-slate-300'
                        }`}
                      >
                        {model === currentModel && '✓ '}{model}
                      </button>
                    ))}
                  </div>
                </>
              )}
            </div>
          )}
        </div>
        <div className="flex items-center gap-2">
          <button
            onClick={() => { const next = !searchOpen; setSearchOpen(next); if (next) { setSearchQuery(''); setSearchResults([]); setTimeout(() => searchInputRef.current?.focus(), 0) } }}
            className="text-sm text-slate-400 hover:text-white transition-colors p-1"
            title="搜索 (Ctrl+K)"
          >
            🔍
          </button>
          <button
            onClick={() => setSettingsOpen(true)}
            className="text-sm text-slate-400 hover:text-white transition-colors p-1"
            title="设置"
          >
            ⚙️
          </button>
          <button
            onClick={handleLogout}
            className="text-sm text-slate-400 hover:text-white transition-colors p-1"
          >
            Logout
          </button>
        </div>
      </header>

      {/* Search panel */}
      {searchOpen && (
        <div className="bg-slate-800/95 border-b border-slate-700 px-4 py-3 backdrop-blur-sm">
          <div className="max-w-2xl mx-auto">
            <div className="relative">
              <input
                ref={searchInputRef}
                type="text"
                value={searchQuery}
                onChange={e => setSearchQuery(e.target.value)}
                onKeyDown={e => { if (e.key === 'Escape') setSearchOpen(false) }}
                placeholder="搜索消息历史..."
                autoFocus
                className="w-full px-4 py-2 bg-slate-700 border border-slate-600 rounded-lg text-sm text-white placeholder-slate-400 focus:outline-none focus:border-blue-500"
              />
              {searchLoading && <span className="absolute right-3 top-1/2 -translate-y-1/2 text-xs text-slate-400">搜索中...</span>}
            </div>
            {searchResults.length > 0 && (
              <div className="mt-2 max-h-64 overflow-y-auto space-y-1">
                {searchResults.map(hit => (
                  <div
                    key={hit.id}
                    className="px-3 py-2 rounded-lg bg-slate-700/50 hover:bg-slate-700 cursor-pointer text-sm"
                    onClick={() => {
                      setSearchOpen(false)
                      const el = messagesContainerRef.current?.querySelector(`[data-msg-id="hist-${hit.id}"]`)
                      if (el) {
                        el.scrollIntoView({ behavior: 'smooth', block: 'center' })
                        el.classList.add('search-highlight')
                        setTimeout(() => el.classList.remove('search-highlight'), 2000)
                      }
                    }}
                  >
                    <div className="flex items-center gap-2 mb-1">
                      <span className="text-xs font-medium text-slate-400">{hit.role === 'user' ? '👤' : '🤖'}</span>
                      {hit.created_at && <span className="text-xs text-slate-500">{new Date(hit.created_at).toLocaleString('zh-CN')}</span>}
                    </div>
                    <div className="text-slate-300 text-xs line-clamp-2 whitespace-pre-wrap break-words">
                      {hit.snippet}
                    </div>
                  </div>
                ))}
              </div>
            )}
            {searchQuery && !searchLoading && searchResults.length === 0 && (
              <div className="mt-2 text-center text-xs text-slate-500">未找到匹配结果</div>
            )}
          </div>
        </div>
      )}

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
          <div className="text-center py-20">
            <div className="text-4xl mb-3 opacity-40">🤖</div>
            <p className="text-slate-500 text-sm">开始一段对话</p>
            <p className="text-slate-600 text-xs mt-1">发送消息开始与 AI 助手交流</p>
          </div>
        )}

        {(() => {
          const turns = groupMessagesIntoTurns(messages)
          const eagerCount = 6 // always render last N turns (avoids lazy-load vs scroll conflict)
          return turns.map((turn, i) => {
            const isLatestTurn = i === turns.length - 1
            const isEager = i >= turns.length - eagerCount
            // Only bind live progress to the latest assistant turn if we are
            // actively processing a request. After a page refresh the last
            // historical assistant turn would otherwise steal the progress.
            const isActive = loading || progress !== null
            if (turn.type === 'user') {
		              const content = (
		                <div className="flex justify-end" data-msg-id={turn.message.id}>
		                  <div className="max-w-[80%] rounded-xl px-4 py-3 bg-blue-600 text-white markdown-body text-sm">
			                    <UserMessageContent content={turn.message.content} />
		                    {turn.message.ts && (
		                      <div className="text-xs mt-1 text-right text-blue-200/50">
		                        {formatTime(turn.message.ts)}
		                      </div>
		                    )}
		                  </div>
		                </div>
		              )

              return (isLatestTurn || isEager) ? (
                <div key={turn.message.id}>{content}</div>
              ) : (
                <LazyTurn key={turn.message.id}>{content}</LazyTurn>
              )
            }
            // Assistant turn — last one gets live progress & loading
            const turnSavedProgress = turn.messages[turn.messages.length - 1]?.savedProgress ?? null
            const assistantContent = (
              <div data-msg-id={turn.messages[0].id} key={turn.messages[0].id}>
                <AssistantTurn
                  messages={turn.messages}
                  progress={isLatestTurn && isActive ? progress : null}
                  liveIterations={isLatestTurn && isActive ? liveIterations : undefined}
                  loading={isLatestTurn && isActive && loading}
                  savedProgress={turnSavedProgress}
                />
              </div>
            )
            return (isLatestTurn || isEager) ? assistantContent : (
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
          onClick={() => { setAutoScroll(true); requestAnimationFrame(() => scrollToBottom('smooth')) }}
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
              currentModel={currentModel}
              onCancel={handleCancel}
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

      {/* Toast notifications */}
      <div className="fixed top-4 right-4 z-50 space-y-2">
        {toasts.map(toast => (
          <div
            key={toast.id}
            className={`px-4 py-2 rounded-lg shadow-lg text-sm toast-enter ${
              toast.type === 'error' ? 'bg-red-500/90 text-white' :
              toast.type === 'success' ? 'bg-green-500/90 text-white' :
              'bg-slate-700/90 text-slate-200 border border-slate-600'
            }`}
          >
            {toast.message}
          </div>
        ))}
      </div>

      {/* AskUser interaction panel */}
      {askUser && (
        <div className="fixed inset-0 bg-black/50 flex items-center justify-center z-50 askuser-backdrop" onClick={(e) => {
          if (e.target === e.currentTarget) {
            // Cancel on backdrop click
            if (wsRef.current?.readyState === WebSocket.OPEN) {
              wsRef.current.send(JSON.stringify({
                type: 'ask_user_response',
                answers: askUser.answers,
                cancelled: true,
              }))
            } else {
              showToast('连接已断开，请刷新页面', 'error')
            }
            setAskUser(null)
          }
        }}>
          <div className="bg-slate-800 border border-slate-600 rounded-2xl shadow-2xl max-w-lg w-full mx-4 askuser-panel">
            <div className="px-5 py-4 border-b border-slate-700 flex items-center justify-between">
              <h3 className="text-sm font-semibold text-white flex items-center gap-2">
                <span className="text-lg">🤔</span>
                Agent 需要你的输入
              </h3>
              <span className="text-xs text-slate-400">
                {askUser.currentQ + 1} / {askUser.questions.length}
              </span>
            </div>
            <div className="px-5 py-4">
              <p className="text-sm text-slate-200 mb-4">{askUser.questions[askUser.currentQ].question}</p>
              {askUser.questions[askUser.currentQ].options && askUser.questions[askUser.currentQ].options!.length > 0 ? (
                <div className="space-y-2">
                  {askUser.questions[askUser.currentQ].options!.map((opt, i) => (
                    <button
                      key={i}
                      onClick={() => submitAnswer(opt)}
                      className="w-full text-left px-4 py-2.5 rounded-lg border border-slate-600 text-sm text-slate-200 hover:bg-blue-500/10 hover:border-blue-500/50 transition-colors"
                    >
                      {opt}
                    </button>
                  ))}
                </div>
              ) : (
                <div className="flex gap-2">
                  <input
                    type="text"
                    ref={askUserInputRef}
                    autoFocus
                    placeholder="输入你的回答..."
                    className="flex-1 px-3 py-2 bg-slate-700 border border-slate-600 rounded-lg text-sm text-white placeholder-slate-400 focus:outline-none focus:border-blue-500"
                    onKeyDown={(e) => {
                      if (e.key === 'Enter') {
                        submitAnswer((e.target as HTMLInputElement).value)
                      }
                    }}
                  />
                  <button
                    onClick={() => submitAnswer(askUserInputRef.current?.value || '')}
                    className="px-4 py-2 bg-blue-600 hover:bg-blue-500 text-white text-sm rounded-lg transition-colors"
                  >
                    提交
                  </button>
                </div>
              )}
            </div>
            <div className="px-5 py-3 border-t border-slate-700 flex justify-between items-center">
              {askUser.currentQ > 0 ? (
                <button
                  onClick={() => setAskUser({ ...askUser, currentQ: askUser.currentQ - 1 })}
                  className="text-xs text-slate-400 hover:text-white transition-colors"
                >
                  ← 上一题
                </button>
              ) : (
                <div />
              )}
              <button
                onClick={() => {
                  if (wsRef.current?.readyState === WebSocket.OPEN) {
                    wsRef.current.send(JSON.stringify({
                      type: 'ask_user_response',
                      answers: askUser.answers,
                      cancelled: true,
                    }))
                  } else {
                    showToast('连接已断开，请刷新页面', 'error')
                  }
                  setAskUser(null)
                }}
                className="text-xs text-red-400 hover:text-red-300 transition-colors"
              >
                取消
              </button>
            </div>
          </div>
        </div>
      )}

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
