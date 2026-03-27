import { useState, memo } from 'react'
import Markdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import { getCodeBlockProps } from './CodeBlock'
import type { WsProgressPayload, IterationSnapshot, WsToolProgress } from './ProgressPanel'
import { CompletedIteration } from './ProgressPanel'

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

function ToolItem({ tool }: { tool: WsToolProgress }) {
  const isRunning = tool.status === 'running'
  const icon = isRunning ? '⚡' : tool.status === 'done' ? '✅' : '❌'

  return (
    <div className={`flex items-center gap-2 px-3 py-1.5 text-sm ${isRunning ? 'text-blue-300' : 'text-slate-400'}`}>
      <span className={isRunning ? 'tool-pulse' : ''}>{icon}</span>
      <span className="font-mono text-xs flex-1 truncate">{tool.label || tool.name}</span>
      {tool.elapsed_ms > 0 && (
        <span className="text-xs text-slate-500 font-mono">{formatElapsed(tool.elapsed_ms)}</span>
      )}
    </div>
  )
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
  if (trimmed.startsWith('💭')) return true
  if (trimmed.startsWith('<think')) return true
  if (trimmed.startsWith('<thinking')) return true
  if (trimmed.startsWith('【思考】')) return true
  // Must have substantial thinking content (>10 chars) after the marker
  // to avoid false positives on short 💭 prefixes
  if (trimmed.startsWith('💭') && trimmed.length > 12) return true
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
  const hasActiveTools = loading && (progress?.active_tools?.length ?? 0) > 0
  const allTools = effectiveProgress ? [
    ...(effectiveProgress.completed_tools ?? []),
    ...(loading ? (progress?.active_tools ?? []) : []),
  ] : []
  const totalToolCount = allTools.length

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
        })),
      }].sort((a, b) => a.iteration - b.iteration)
    }
  }

  const currentThinking = (progress?.thinking || '').trim()
  const seenThinkings = new Set(displayLiveIterations.map(s => (s.thinking || '').trim()).filter(Boolean))
  const shouldShowCurrentThinking = currentThinking.length > 0 && !seenThinkings.has(currentThinking)

  return (
    <div className="flex justify-start msg-fade-in">
      <div className="assistant-turn-container">
        {/* Collapsible: Thinking section */}
        {thinkingMsgs.length > 0 && (
          <CollapsibleSection icon="💭" title="思考过程" badge={thinkingMsgs.length}>
            <div className="space-y-2">
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
                      <div key={`${snap.iteration}-${i}`} className="flex items-center gap-2 px-2 py-1 text-sm">
                        <span>{tool.status === 'error' ? '❌' : '✅'}</span>
                        <span className="font-mono text-xs text-slate-400 flex-1 truncate">
                          {tool.label || tool.name}
                        </span>
                        {tool.elapsed_ms != null && tool.elapsed_ms > 0 && (
                          <span className="text-xs text-slate-500 font-mono">{formatElapsed(tool.elapsed_ms)}</span>
                        )}
                      </div>
                    ))}
                  </div>
                </div>
              ))}
            </div>
          </CollapsibleSection>
        )}

        {/* Collapsible: Tool calls section (from completed progress or live) */}
        {/* Hidden when iterationHistory exists — iteration history already shows all tools */}
        {totalToolCount > 0 && !messages[0]?.iterationHistory && (
          <CollapsibleSection
            icon="⚡"
            title="工具调用"
            badge={totalToolCount}
            defaultOpen={loading && hasActiveTools}
          >
            <div className="divide-y divide-slate-700/30">
              {allTools.map((tool, i) => (
                <ToolItem key={`${tool.name}-${i}`} tool={tool} />
              ))}
            </div>
          </CollapsibleSection>
        )}

        {/* Live progress — current iteration thinking + active tools (only during loading) */}
        {loading && progress && progress.phase !== 'done' && (
          <div className="mb-2 rounded border border-slate-700/30 overflow-hidden">
            <div className="px-3 pt-2 text-[11px] text-slate-600/90 font-mono text-right">
              #{progress.iteration}
            </div>
            {shouldShowCurrentThinking && (
              <ThinkingBlock content={progress.thinking} />
            )}
            {(progress.active_tools?.length ?? 0) > 0 && (
              <div className="divide-y divide-slate-700/30">
                {progress.active_tools!.map((tool, i) => (
                  <div key={`active-${i}`} className="flex items-center gap-2 px-3 py-1.5 text-sm">
                    <span className="tool-pulse text-xs">⏳</span>
                    <span className="font-mono text-xs text-blue-300 flex-1 truncate">
                      {tool.label || tool.name}
                    </span>
                    {tool.elapsed_ms > 0 && (
                      <span className="text-xs text-slate-500 font-mono">{formatElapsed(tool.elapsed_ms)}</span>
                    )}
                  </div>
                ))}
              </div>
            )}
          </div>
        )}

        {/* Collapsible: Iteration history (from saved snapshots) */}
        {!loading && messages[0]?.iterationHistory && messages[0].iterationHistory.length > 0 && (
          <CollapsibleSection
            icon="📋"
            title="迭代过程"
            badge={messages[0].iterationHistory!.length}
          >
            <div className="divide-y divide-slate-700/30">
              {messages[0].iterationHistory!.map((snap) => (
                <CompletedIteration key={snap.iteration} snap={snap} />
              ))}
            </div>
          </CollapsibleSection>
        )}

        {/* Main text content — always visible */}
        {textMsgs.length > 0 && (
          <div className="assistant-turn-content">
            {textMsgs.map((msg) => (
              <div key={msg.id} className="markdown-body">
                <Markdown components={codeBlockComponents} remarkPlugins={[remarkGfm]}>
                  {msg.content}
                </Markdown>
              </div>
            ))}
          </div>
        )}

        {/* Loading pulse when no content yet */}
        {loading && textMsgs.length === 0 && totalToolCount === 0 && !phaseIcon && (
          <div className="assistant-turn-loading">
            <div className="flex gap-1">
              <span className="w-2 h-2 bg-slate-500 rounded-full animate-bounce" style={{ animationDelay: '0ms' }} />
              <span className="w-2 h-2 bg-slate-500 rounded-full animate-bounce" style={{ animationDelay: '150ms' }} />
              <span className="w-2 h-2 bg-slate-500 rounded-full animate-bounce" style={{ animationDelay: '300ms' }} />
            </div>
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
