interface WsToolProgress {
  name: string
  label: string
  status: string   // running | done | error
  elapsed_ms: number
}

interface WsProgressPayload {
  phase: string           // thinking | tool_exec | compressing | retrying | done
  iteration: number
  active_tools: WsToolProgress[]
  completed_tools: WsToolProgress[]
  thinking: string        // current iteration's thinking content
}

export interface IterationSnapshot {
  iteration: number
  thinking?: string
  tools: IterationToolSnapshot[]
}

export interface IterationToolSnapshot {
  name: string
  label?: string
  status: string   // done | error
  elapsed_ms?: number
}

interface ProgressPanelProps {
  progress: WsProgressPayload | null
  liveIterations?: IterationSnapshot[]
  loading: boolean
}

function formatElapsed(ms: number): string {
  if (ms < 1000) return `${ms}ms`
  return `${(ms / 1000).toFixed(1)}s`
}

/** A completed iteration: thinking + concrete tool list */
export function CompletedIteration({ snap }: { snap: IterationSnapshot }) {
  return (
    <div className="px-3 py-2 border-b border-slate-700/30 last:border-b-0">
      <div className="flex items-center gap-1 text-[11px] text-slate-600/90 font-mono mb-1">

        <span>#{snap.iteration}</span>
      </div>
      {snap.thinking && (
        <div className="px-2 py-1 mb-1 text-xs text-slate-400 italic whitespace-pre-wrap break-words">
          {snap.thinking}
        </div>
      )}
      <div className="space-y-0.5">
        {(snap.tools ?? []).map((tool, i) => {
          const icon = tool.status === 'error' ? '❌' : '✅'
          return (
            <div key={`${snap.iteration}-${i}`} className="flex items-center gap-2 px-2 py-1 text-sm">
              <span>{icon}</span>
              <span className="font-mono text-xs text-slate-400 flex-1 truncate">
                {tool.label || tool.name}
              </span>
              {tool.elapsed_ms != null && tool.elapsed_ms > 0 && (
                <span className="text-xs text-slate-500 font-mono">{formatElapsed(tool.elapsed_ms)}</span>
              )}
            </div>
          )
        })}
      </div>
    </div>
  )
}

export default function ProgressPanel({ progress, liveIterations, loading }: ProgressPanelProps) {
  // Fallback: simple bouncing dots when no structured data (old backend compat)
  if (!progress && loading) {
    return (
      <div className="flex justify-start">
        <div className="bg-slate-800 border border-slate-700 rounded-xl px-4 py-3">
          <div className="flex gap-1">
            <span className="w-2 h-2 bg-slate-500 rounded-full animate-bounce" style={{ animationDelay: '0ms' }} />
            <span className="w-2 h-2 bg-slate-500 rounded-full animate-bounce" style={{ animationDelay: '150ms' }} />
            <span className="w-2 h-2 bg-slate-500 rounded-full animate-bounce" style={{ animationDelay: '300ms' }} />
          </div>
        </div>
      </div>
    )
  }

  if (!progress) return null

  const isActive = progress.phase !== 'done'

  const baseLiveIterations = liveIterations ?? []
  let displayLiveIterations = baseLiveIterations
  if (progress.iteration > 0 && (progress.completed_tools?.length ?? 0) > 0) {
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

  // Show active tools (pending/running). Completed ones are rendered in completed iterations.
  const activeTools = progress.active_tools?.filter(t => t.status !== 'done' && t.status !== 'error') ?? []
  const hasActiveTools = activeTools.length > 0
  const currentThinking = (progress.thinking || '').trim()
  const seenThinkings = new Set(displayLiveIterations.map(s => (s.thinking || '').trim()).filter(Boolean))
  const shouldShowCurrentThinking = currentThinking.length > 0 && !seenThinkings.has(currentThinking)

  return (
    <div className="flex justify-start progress-fade-in">
      <div className={`max-w-[80%] w-full rounded-xl border overflow-hidden ${
        isActive ? 'border-blue-800/50 bg-slate-800/90 progress-panel-active' : 'border-slate-700 bg-slate-800'
      }`}>

        {/* Completed iterations stacked above */}
        {(displayLiveIterations.length ?? 0) > 0 && (
          <div className="divide-y divide-slate-700/30">
            {displayLiveIterations.map(snap => (
              <CompletedIteration key={snap.iteration} snap={snap} />
            ))}
          </div>
        )}

        {/* Current iteration */}
        <div className="px-3 py-2">
          <div className="flex items-center text-[11px] text-slate-600/90 font-mono mb-1">
            <span className="ml-auto">#{progress.iteration}</span>
          </div>

          {/* Thinking content — render whenever present, regardless of phase */}
          {shouldShowCurrentThinking && (
            <div className="px-2 py-1 mb-1 text-xs text-slate-400 italic whitespace-pre-wrap break-words">
              {progress.thinking}
            </div>
          )}

          {/* Thinking phase indicator — show when no content yet */}
          {progress.phase === 'thinking' && !progress.thinking && (
            <div className="flex items-center gap-1.5 text-xs text-slate-500">
              <span>💭</span>
              <span className="flex-1">thinking…</span>
            </div>
          )}

          {/* Running tools — show ALL with hourglass */}
          {hasActiveTools && activeTools.map((tool, i) => (
            <div key={`${tool.name}-${i}`} className="flex items-center gap-2 text-sm">
              <span className="tool-pulse">⏳</span>
              <span className="font-mono text-xs flex-1 truncate text-blue-300">
                {tool.label || tool.name}
              </span>
              {tool.elapsed_ms > 0 && (
                <span className="text-xs text-slate-500 font-mono shrink-0">{formatElapsed(tool.elapsed_ms)}</span>
              )}
            </div>
          ))}

          {/* No running tools in tool_exec phase — show last completed tool */}
          {!hasActiveTools && progress.phase === 'tool_exec' && (() => {
            const completed = progress.completed_tools ?? []
            const last = completed.length > 0 ? completed[completed.length - 1] : null
            if (!last) {
              return (
                <div className="flex items-center gap-1.5 text-xs text-slate-500 font-mono">
                  <span className="flex-1">called 0 tools</span>
                </div>
              )
            }
            const icon = last.status === 'done' ? '✅' : '❌'
            return (
              <div className="flex items-center gap-2 text-sm">
                <span>{icon}</span>
                <span className="font-mono text-xs flex-1 truncate text-slate-400">
                  {last.label || last.name}
                </span>
                {last.elapsed_ms > 0 && (
                  <span className="text-xs text-slate-600 font-mono shrink-0">{formatElapsed(last.elapsed_ms)}</span>
                )}
              </div>
            )
          })()}

          {/* Other phases without tools (compressing, retrying) */}
          {!hasActiveTools && progress.phase !== 'thinking' && progress.phase !== 'tool_exec' && progress.phase !== 'done' && (
            <div className="flex items-center gap-1.5 text-xs text-slate-500">
              <span>{progress.phase === 'compressing' ? '📦' : '🔄'}</span>
              <span>{progress.phase}…</span>
            </div>
          )}
        </div>
      </div>
    </div>
  )
}

export type { WsProgressPayload, WsToolProgress }
