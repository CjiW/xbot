import { useState } from 'react'
import Markdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import { getCodeBlockProps } from './CodeBlock'
import type { WsProgressPayload, WsToolProgress } from './ProgressPanel'

interface Message {
  id: string
  type: 'user' | 'assistant' | 'system'
  content: string
  ts?: number
}

interface AssistantTurnProps {
  messages: Message[]
  progress: WsProgressPayload | null
  loading: boolean
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
  if (trimmed.startsWith('💭')) return true
  if (trimmed.startsWith('<think')) return true
  if (trimmed.startsWith('【思考】')) return true
  // Long first-person reasoning patterns (common in AI thinking)
  if (/^(Let me|Let's|I need to|I'll|I should|Let's think)/i.test(trimmed)) return true
  return false
}

export default function AssistantTurn({ messages, progress, loading }: AssistantTurnProps) {
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

  // Gather tool info from progress
  const hasActiveTools = (progress?.active_tools?.length ?? 0) > 0
  const hasCompletedTools = (progress?.completed_tools?.length ?? 0) > 0
  const allTools: WsToolProgress[] = [
    ...(progress?.completed_tools ?? []),
    ...(progress?.active_tools ?? []),
  ]
  const totalToolCount = allTools.length

  // Determine phase display
  const phaseIcon = progress?.phase === 'thinking' ? '💭'
    : progress?.phase === 'tool_exec' ? '⚡'
    : progress?.phase === 'compressing' ? '📦'
    : progress?.phase === 'retrying' ? '🔄'
    : progress?.phase === 'done' ? '✅'
    : null

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

        {/* Collapsible: Tool calls section (from completed progress or live) */}
        {totalToolCount > 0 && (
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

        {/* Live progress indicator (when still loading and no tools yet) */}
        {loading && totalToolCount === 0 && phaseIcon && (
          <div className="assistant-turn-live-phase">
            <span className={progress?.phase !== 'done' ? 'tool-pulse' : ''}>{phaseIcon}</span>
            <span className="text-xs text-slate-400">
              {progress?.phase === 'thinking' ? '思考中...'
                : progress?.phase === 'compressing' ? '压缩上下文...'
                : progress?.phase === 'retrying' ? '重试中...'
                : '处理中...'}
            </span>
          </div>
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
