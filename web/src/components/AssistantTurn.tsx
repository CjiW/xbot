import { useState, useEffect, useRef, memo } from 'react'
import Markdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import { getCodeBlockProps } from './CodeBlock'
import type { WsProgressPayload, IterationSnapshot } from './ProgressPanel'
import { CompletedIteration, BouncingDots, SubAgentTree } from './ProgressPanel'


const COLLAPSE_THRESHOLD = 20 // lines

function CollapsibleMessage({ children }: { children: React.ReactNode }) {
  const [collapsed, setCollapsed] = useState(true)
  const ref = useRef<HTMLDivElement>(null)
  const [tooLong, setTooLong] = useState(false)

  useEffect(() => {
    if (ref.current) {
      // Use scrollHeight for accurate measurement even when max-h clips the content
      const lineHeight = parseFloat(getComputedStyle(ref.current).lineHeight) || 20
      const lineCount = Math.ceil(ref.current.scrollHeight / lineHeight)
      setTooLong(lineCount > COLLAPSE_THRESHOLD)
    }
  }, [children])

  if (!tooLong) return <>{children}</>

  return (
    <div ref={ref}>
      <div
        className={`relative ${collapsed ? 'max-h-80 overflow-hidden' : ''}`}
      >
        {children}
        {collapsed && (
          <div className="absolute bottom-0 left-0 right-0 h-16 bg-gradient-to-t from-slate-800/90 to-transparent pointer-events-none" />
        )}
      </div>
      <button
        onClick={() => setCollapsed(!collapsed)}
        className="mt-1 text-xs text-slate-500 hover:text-slate-300 transition-colors flex items-center gap-1"
      >
        <span className={`transition-transform ${collapsed ? '' : 'rotate-90'}`}>▸</span>
        {collapsed ? '展开全部' : '折叠'}
      </button>
    </div>
  )
}
interface Message {
  id: string
  type: 'user' | 'assistant' | 'system'
  content: string
  ts?: number
  iterationHistory?: IterationSnapshot[] | null
}


// Memoized thinking display — only re-renders when content actually changes
const ThinkingBlock = memo(({ content }: { content: string }) => (
  <div className="px-3 py-2 text-xs text-slate-400 italic whitespace-pre-wrap break-words">
    {content}
  </div>
))

interface AssistantTurnProps {
  messages: Message[]
  progress: WsProgressPayload | null
  liveIterations?: IterationSnapshot[]
  loading: boolean
  // Saved progress from a completed response (for showing intermediate process collapsed)
  savedProgress?: WsProgressPayload | null
}


const codeBlockComponents = getCodeBlockProps()

function formatElapsed(ms: number): string {
  if (ms < 1000) return `${ms}ms`
  return `${(ms / 1000).toFixed(1)}s`
}

/** Collapsible section with a header bar */
function CollapsibleSection({
  icon,
  title,
  badge,
  defaultOpen = false,
  children,
  className = '',
}: {
  icon: string
  title: string
  badge?: string | number
  defaultOpen?: boolean
  children: React.ReactNode
  className?: string
}) {
  const [open, setOpen] = useState(defaultOpen)

  return (
    <div className={`assistant-turn-section ${className}`}>
      <button
        className="assistant-turn-section-header"
        onClick={() => setOpen(!open)}
      >
        <span className="flex items-center gap-2">
          <span>{icon}</span>
          <span className="text-xs text-slate-400 font-medium">{title}</span>
          {badge !== undefined && (
            <span className="text-xs text-slate-500 font-mono">({badge})</span>
          )}
        </span>
        <span className={`assistant-turn-chevron ${open ? 'assistant-turn-chevron-open' : ''}`}>▸</span>
      </button>
      {open && (
        <div className="assistant-turn-section-body">
          {children}
        </div>
      )}
    </div>
  )
}

/**
 * Detect if a message content looks like "thinking" output.
 * Heuristic: starts with 💭 or is wrapped in <think/> tags or contains
 * typical thinking markers.
 */
function isThinkingContent(content: string): boolean {
  const trimmed = content.trim()
  // Only match explicit thinking markers — NOT regular assistant text
  if (trimmed.startsWith('💭') && trimmed.length > 12) return true
  if (trimmed.startsWith('<think')) return true
  if (trimmed.startsWith('<thinking')) return true
  if (trimmed.startsWith('【思考】')) return true
  return false
}

export default function AssistantTurn({ messages, progress, liveIterations, loading, savedProgress }: AssistantTurnProps) {
  // Classify messages
  const thinkingMsgs: Message[] = []
  const textMsgs: Message[] = []

  for (const msg of messages) {
    if (isThinkingContent(msg.content)) {
      thinkingMsgs.push(msg)
    } else {
      textMsgs.push(msg)
    }
  }

  // Use live progress when loading, fall back to savedProgress for completed turns
  const effectiveProgress = loading ? progress : (savedProgress ?? null)
  const hasTools = effectiveProgress
    ? (effectiveProgress.completed_tools?.length ?? 0) + (loading ? (progress?.active_tools?.length ?? 0) : 0) > 0
    : false

  // Determine phase display
  const phaseIcon = effectiveProgress?.phase === 'thinking' ? '💭'
    : effectiveProgress?.phase === 'tool_exec' ? '⚡'
    : effectiveProgress?.phase === 'compressing' ? '📦'
    : effectiveProgress?.phase === 'retrying' ? '🔄'
    : effectiveProgress?.phase === 'done' ? '✅'
    : null

  const baseLiveIterations = liveIterations ?? []
  let displayLiveIterations = baseLiveIterations
  if (progress && progress.iteration > 0 && (progress.completed_tools?.length ?? 0) > 0) {
    const prevIteration = progress.iteration - 1
    const hasPrev = baseLiveIterations.some((s) => s.iteration === prevIteration)
    if (!hasPrev) {
      displayLiveIterations = [...baseLiveIterations, {
        iteration: prevIteration,
        tools: (progress.completed_tools ?? []).map((t) => ({
          name: t.name,
          label: t.label,
          status: t.status,
          elapsed_ms: t.elapsed_ms,
          summary: t.summary,
        })),
      }].sort((a, b) => a.iteration - b.iteration)
    }
  }

  const currentThinking = (progress?.thinking || '').trim()
  const seenThinkings = new Set(displayLiveIterations.map(s => (s.thinking || '').trim()).filter(Boolean))
  const shouldShowCurrentThinking = currentThinking.length > 0 && !seenThinkings.has(currentThinking)

  return (
    <div className="flex justify-start">
      <div className="assistant-turn-container">
        {/* Collapsible: Thinking section */}
        {thinkingMsgs.length > 0 && (
          <CollapsibleSection icon="💭" title="思考过程" badge={thinkingMsgs.length} className="thinking-section">
            <div className="space-y-2 pl-1">
              {thinkingMsgs.map((msg) => (
                <div key={msg.id} className="text-sm text-slate-400 italic">
                  <Markdown components={codeBlockComponents} remarkPlugins={[remarkGfm]}>
                    {msg.content.replace(/^💭\s*/, '')}
                  </Markdown>
                </div>
              ))}
            </div>
          </CollapsibleSection>
        )}

        {/* Live completed iterations (during loading) */}
        {loading && (displayLiveIterations.length ?? 0) > 0 && (
          <CollapsibleSection
            icon="📋"
            title="迭代过程"
            badge={displayLiveIterations.length}
            defaultOpen={true}
          >
            <div className="divide-y divide-slate-700/30">
              {displayLiveIterations.map(snap => (
                <div key={snap.iteration} className="px-3 py-2">
                  <div className="text-[11px] text-slate-600/90 font-mono mb-1">
                    #{snap.iteration}
                  </div>
                  {snap.thinking && (
                    <div className="px-2 py-1 mb-1 text-xs text-slate-400 italic whitespace-pre-wrap break-words">
                      {snap.thinking}
                    </div>
                  )}
                  <div className="space-y-0.5">
                    {snap.tools.map((tool, i) => (
                      <div key={`${snap.iteration}-${i}`} className="px-2 py-1 text-sm">
                        <div className="flex items-center gap-2">
                          <span>{tool.status === 'error' ? '❌' : '✅'}</span>
                          <span className="font-mono text-xs text-slate-400 flex-1 truncate">
                            {tool.label || tool.name}
                          </span>
                          {tool.elapsed_ms != null && tool.elapsed_ms > 0 && (
                            <span className="text-xs text-slate-500 font-mono">{formatElapsed(tool.elapsed_ms)}</span>
                          )}
                        </div>
                        {tool.summary && (
                          <div className="text-[10px] text-slate-500 truncate pl-5 mt-0.5">{tool.summary}</div>
                        )}
                      </div>
                    ))}
                  </div>
                </div>
              ))}
            </div>
          </CollapsibleSection>
        )}



        {/* Live progress — current iteration (only during loading) */}
        {loading && progress && progress.phase !== 'done' && (
          <div className="mb-2 rounded border border-slate-700/30 overflow-hidden">
            <div className="px-3 py-2">
              <div className="text-[11px] text-slate-600/90 font-mono mb-1">
                #{progress.iteration}
              </div>
              {shouldShowCurrentThinking && (
                <ThinkingBlock content={progress.thinking} />
              )}
              {(progress.active_tools?.length ?? 0) > 0 ? (
                <div className="space-y-0.5">
                  {progress.active_tools!.map((tool, i) => (
                    <div key={`active-${i}`} className="flex items-center gap-2 px-2 py-1 text-sm">
                      <span className="tool-pulse">⏳</span>
                      <span className="font-mono text-xs text-slate-400 flex-1 truncate">
                        {tool.label || tool.name}
                      </span>
                      {tool.elapsed_ms > 0 && (
                        <span className="text-xs text-slate-500 font-mono">{formatElapsed(tool.elapsed_ms)}</span>
                      )}
                    </div>
                  ))}
                </div>
              ) : !shouldShowCurrentThinking ? (
                <div className="flex items-center gap-2 px-2 py-1">
                  <BouncingDots />
                  <span className="text-xs text-slate-500 italic">
                    {progress.phase === 'thinking' ? '思考中...' : progress.phase === 'tool_exec' ? '执行工具...' : '处理中...'}
                  </span>
                </div>
              ) : null}
              {/* SubAgent tree */}
              {progress.sub_agents && progress.sub_agents.length > 0 && (
                <div className="mt-2 pt-2 border-t border-slate-700/30">
                  <SubAgentTree agents={progress.sub_agents} />
                </div>
              )}
            </div>
          </div>
        )}

        {/* Loading but no progress yet — show animated placeholder */}
        {loading && (!progress || progress.phase === 'done') && (
          <div className="mb-2 rounded border border-slate-700/30 overflow-hidden">
            <div className="px-3 py-2">
              <BouncingDots text="准备中..." />
            </div>
          </div>
        )}



        {/* Collapsible: Iteration history (from saved snapshots) */}
        {!loading && messages.length > 0 && messages[messages.length - 1]?.iterationHistory && messages[messages.length - 1].iterationHistory!.length > 0 && (
          <CollapsibleSection
            icon="📋"
            title="迭代过程"
            badge={messages[messages.length - 1].iterationHistory!.length}
          >
            <div className="divide-y divide-slate-700/30">
              {messages[messages.length - 1].iterationHistory!.map((snap) => (
                <CompletedIteration key={snap.iteration} snap={snap} />
              ))}
            </div>
          </CollapsibleSection>
        )}

        {/* Main text content — always visible */}
        {textMsgs.length > 0 && (
          <div className="assistant-turn-content">
            <CollapsibleMessage>
              {textMsgs.map((msg) => (
                <div key={msg.id} className="markdown-body">
                  <Markdown components={codeBlockComponents} remarkPlugins={[remarkGfm]}>
                    {msg.content}
                  </Markdown>
                </div>
              ))}
            </CollapsibleMessage>
          </div>
        )}

        {/* Loading pulse when no content yet and no iteration/progress placeholders */}
        {loading && textMsgs.length === 0 && !hasTools && !phaseIcon && !progress && (
            <div className="thinking-orb thinking-orb-sm">
              <div className="thinking-orb-ring thinking-orb-ring-1" />
              <div className="thinking-orb-ring thinking-orb-ring-2" />
              <div className="thinking-orb-core" />
            </div>
        )}

        {/* Loading indicator at bottom of content when still streaming */}
        {loading && textMsgs.length > 0 && (
          <div className="assistant-turn-streaming-indicator">
            <span className="assistant-turn-cursor" />
          </div>
        )}
      </div>
    </div>
  )
}
