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
}

interface ProgressPanelProps {
  progress: WsProgressPayload | null
  loading: boolean
}

const phaseIcons: Record<string, string> = {
  thinking: '💭',
  tool_exec: '⚡',
  compressing: '📦',
  retrying: '🔄',
  done: '✅',
}

const phaseLabels: Record<string, string> = {
  thinking: '思考中...',
  tool_exec: '执行工具...',
  compressing: '压缩上下文...',
  retrying: '重试中...',
  done: '完成',
}

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

export default function ProgressPanel({ progress, loading }: ProgressPanelProps) {
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

  const phaseIcon = phaseIcons[progress.phase] || '⏳'
  const phaseLabel = phaseLabels[progress.phase] || progress.phase
  const isActive = progress.phase !== 'done'

  return (
    <div className={`flex justify-start progress-fade-in`}>
      <div className={`max-w-[80%] w-full rounded-xl border overflow-hidden ${
        isActive ? 'border-blue-800/50 bg-slate-800/90 progress-panel-active' : 'border-slate-700 bg-slate-800'
      }`}>
        {/* Header */}
        <div className="flex items-center justify-between px-4 py-2 border-b border-slate-700/50">
          <div className="flex items-center gap-2">
            <span>{phaseIcon}</span>
            <span className="text-sm text-slate-300">{phaseLabel}</span>
          </div>
          {progress.iteration > 0 && (
            <span className="text-xs text-slate-500 font-mono">#{progress.iteration}</span>
          )}
        </div>

        {/* Tool list */}
        {(progress.active_tools.length > 0 || progress.completed_tools.length > 0) && (
          <div className="py-1 divide-y divide-slate-700/30">
            {/* Active tools first */}
            {progress.active_tools.map((tool) => (
              <ToolItem key={tool.name} tool={tool} />
            ))}
            {/* Completed tools */}
            {progress.completed_tools.map((tool) => (
              <ToolItem key={tool.name} tool={tool} />
            ))}
          </div>
        )}
      </div>
    </div>
  )
}

export type { WsProgressPayload, WsToolProgress }
