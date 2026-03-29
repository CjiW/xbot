interface WsToolProgress {
  name: string
  label: string
  status: string
  elapsed_ms: number
}

interface WsProgressPayload {
  phase: string
  iteration: number
  active_tools: WsToolProgress[]
  completed_tools: WsToolProgress[]
  thinking: string
}

export interface IterationSnapshot {
  iteration: number
  thinking?: string
  tools: IterationToolSnapshot[]
}

export interface IterationToolSnapshot {
  name: string
  label?: string
  status: string
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

export function BouncingDots({ text }: { text?: string }) {
  return (
    <div className="flex items-center gap-2 px-2 py-1">
      <span className="flex gap-[3px]">
        <span className="w-1.5 h-1.5 bg-slate-500 rounded-full animate-bounce" style={{ animationDelay: '0ms' }} />
        <span className="w-1.5 h-1.5 bg-slate-500 rounded-full animate-bounce" style={{ animationDelay: '150ms' }} />
        <span className="w-1.5 h-1.5 bg-slate-500 rounded-full animate-bounce" style={{ animationDelay: '300ms' }} />
      </span>
      {text && <span className="text-[11px] text-slate-500 italic">{text}</span>}
    </div>
  )
}

export function CompletedIteration({ snap }: { snap: IterationSnapshot }) {
  const hasThinking = !!(snap.thinking || '').trim()
  const hasTools = (snap.tools ?? []).length > 0
  const isEmpty = !hasThinking && !hasTools
  return (
    <div className="px-3 py-2 border-b border-slate-700/30 last:border-b-0">
      <div className="flex items-center gap-1 text-[11px] text-slate-600/90 font-mono mb-1">#{snap.iteration}</div>
      {hasThinking && <div className="px-2 py-1 mb-1 text-xs text-slate-400 italic whitespace-pre-wrap break-words">{snap.thinking}</div>}
      {hasTools && (
        <div className="space-y-0.5">
          {(snap.tools ?? []).map((tool, i) => {
            const icon = tool.status === 'error' ? '❌' : '✅'
            return (
              <div key={`${snap.iteration}-${i}`} className="flex items-center gap-2 px-2 py-1 text-sm">
                <span>{icon}</span>
                <span className="font-mono text-xs text-slate-400 flex-1 truncate">{tool.label || tool.name}</span>
                {tool.elapsed_ms != null && tool.elapsed_ms > 0 && <span className="text-xs text-slate-500 font-mono">{formatElapsed(tool.elapsed_ms)}</span>}
              </div>
            )
          })}
        </div>
      )}
      {isEmpty && <BouncingDots />}
    </div>
  )
}

export default function ProgressPanel({ progress, liveIterations, loading }: ProgressPanelProps) {
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
    if (!baseLiveIterations.some(s => s.iteration === prevIteration)) {
      displayLiveIterations = [...baseLiveIterations, {
        iteration: prevIteration,
        tools: (progress.completed_tools ?? []).map(t => ({ name: t.name, label: t.label, status: t.status, elapsed_ms: t.elapsed_ms })),
      }].sort((a, b) => a.iteration - b.iteration)
    }
  }

  const activeTools = progress.active_tools?.filter(t => t.status !== 'done' && t.status !== 'error') ?? []
  const hasActiveTools = activeTools.length > 0
  const currentThinking = (progress.thinking || '').trim()
  const seenThinkings = new Set(displayLiveIterations.map(s => (s.thinking || '').trim()).filter(Boolean))
  const shouldShowCurrentThinking = currentThinking.length > 0 && !seenThinkings.has(currentThinking)

  // Track whether the current iteration shows any visible content
  const hasVisibleContent = shouldShowCurrentThinking
    || hasActiveTools
    || (progress.phase === 'thinking' && !progress.thinking)
    || (progress.phase === 'tool_exec' && (progress.completed_tools?.length ?? 0) > 0)
    || ['compressing', 'retrying'].includes(progress.phase)

  return (
    <div className="flex justify-start progress-fade-in">
      <div className={`max-w-[80%] w-full rounded-xl border overflow-hidden ${isActive ? 'border-blue-800/50 bg-slate-800/90 progress-panel-active' : 'border-slate-700 bg-slate-800'}`}>
        <div className="divide-y divide-slate-700/30">
          {displayLiveIterations.map(snap => <CompletedIteration key={snap.iteration} snap={snap} />)}

          {isActive && (
            <div className="px-3 py-2">
              <div className="flex items-center gap-1 text-[11px] text-slate-600/90 font-mono mb-1">#{progress.iteration}</div>

              {shouldShowCurrentThinking && (
                <div className="px-2 py-1 mb-1 text-xs text-slate-400 italic whitespace-pre-wrap break-words">{progress.thinking}</div>
              )}

              {progress.phase === 'thinking' && !progress.thinking && <BouncingDots text="thinking…" />}

              {hasActiveTools && activeTools.map((tool, i) => (
                <div key={`${tool.name}-${i}`} className="flex items-center gap-2 px-2 py-1 text-sm">
                  <span className="tool-pulse">⏳</span>
                  <span className="font-mono text-xs text-slate-400 flex-1 truncate">{tool.label || tool.name}</span>
                  {tool.elapsed_ms > 0 && <span className="text-xs text-slate-500 font-mono shrink-0">{formatElapsed(tool.elapsed_ms)}</span>}
                </div>
              ))}

              {!hasActiveTools && progress.phase === 'tool_exec' && (() => {
                const completed = progress.completed_tools ?? []
                const last = completed.length > 0 ? completed[completed.length - 1] : null
                if (!last) return <BouncingDots text="executing…" />
                return (
                  <div className="flex items-center gap-2 px-2 py-1 text-sm">
                    <span>{last.status === 'done' ? '✅' : '❌'}</span>
                    <span className="font-mono text-xs flex-1 truncate text-slate-400">{last.label || last.name}</span>
                    {last.elapsed_ms != null && last.elapsed_ms > 0 && <span className="text-xs text-slate-500 font-mono shrink-0">{formatElapsed(last.elapsed_ms)}</span>}
                  </div>
                )
              })()}

              {['compressing', 'retrying'].includes(progress.phase) && (
                <div className="flex items-center gap-1.5 px-2 py-1 text-xs text-slate-500">
                  <span>{progress.phase === 'compressing' ? '📦' : '🔄'}</span>
                  <span>{progress.phase}…</span>
                </div>
              )}

              {/* Catch-all: nothing matched above → show animated dots */}
              {!hasVisibleContent && <BouncingDots />}
            </div>
          )}
        </div>
      </div>
    </div>
  )
}

export type { WsProgressPayload, WsToolProgress }

