import { useEffect, useState, useCallback, useRef } from 'react'

// ── Types ──

interface RunnerInfo {
  name: string
  token: string
  mode: string
  docker_image: string
  workspace: string
  created_at: string
  online: boolean
}

interface RunnerPanelProps {
  serverUrl?: string
}

// ── Component ──

export default function RunnerPanel({ serverUrl }: RunnerPanelProps) {
  const [runners, setRunners] = useState<RunnerInfo[]>([])
  const [activeRunner, setActiveRunner] = useState<string | null>(null)
  const [loading, setLoading] = useState(true)
  const [actionLoading, setActionLoading] = useState(false)
  const [showAddForm, setShowAddForm] = useState(false)
  const [menuOpen, setMenuOpen] = useState<string | null>(null)
  const [copied, setCopied] = useState<string | null>(null)
  const [deleteConfirm, setDeleteConfirm] = useState<string | null>(null)

  // Add form state
  const [formName, setFormName] = useState('')
  const [formMode, setFormMode] = useState<'native' | 'docker'>('native')
  const [formDockerImage, setFormDockerImage] = useState('ubuntu:22.04')
  const [formWorkspace, setFormWorkspace] = useState('')

  const menuRef = useRef<HTMLDivElement>(null)

  // Close menu on outside click
  useEffect(() => {
    if (!menuOpen) return
    const handleClick = (e: MouseEvent) => {
      if (menuRef.current && !menuRef.current.contains(e.target as Node)) {
        setMenuOpen(null)
      }
    }
    document.addEventListener('mousedown', handleClick)
    return () => document.removeEventListener('mousedown', handleClick)
  }, [menuOpen])

  // Fetch runners
  const fetchRunners = useCallback(async () => {
    try {
      const resp = await fetch('/api/runners')
      const data = await resp.json()
      if (data.ok) {
        setRunners(data.runners || [])
        // Also get active runner
        const activeResp = await fetch('/api/runners/active')
        const activeData = await activeResp.json()
        if (activeData.ok && activeData.runner) {
          setActiveRunner(activeData.runner.name)
        } else {
          setActiveRunner(null)
        }
      }
    } catch {
      // silently fail
    }
    setLoading(false)
  }, [])

  useEffect(() => {
    fetchRunners()
  }, [fetchRunners])

  // Build connect command for a runner
  const buildCommand = useCallback((runner: RunnerInfo) => {
    const wsBase = serverUrl
      ? serverUrl.replace(/^http/, 'ws')
      : `${window.location.protocol === 'https:' ? 'wss:' : 'ws:'}//${window.location.host}`
    let cmd = `./xbot-runner --server ${wsBase}/ws/web-0 --token ${runner.token}`
    if (runner.mode === 'docker' && runner.docker_image) {
      cmd += ` --mode docker --docker-image ${runner.docker_image}`
    }
    return cmd
  }, [serverUrl])

  // Set active
  const handleSetActive = useCallback(async (name: string) => {
    setActionLoading(true)
    try {
      const resp = await fetch(`/api/runners/${encodeURIComponent(name)}/active`, { method: 'PUT' })
      const data = await resp.json()
      if (data.ok) {
        setActiveRunner(name)
      }
    } catch {}
    setActionLoading(false)
  }, [])

  // Copy command
  const handleCopyCommand = useCallback(async (runner: RunnerInfo) => {
    const cmd = buildCommand(runner)
    try {
      await navigator.clipboard.writeText(cmd)
      setCopied(runner.name)
      setTimeout(() => setCopied(null), 2000)
    } catch {}
  }, [buildCommand])

  // Delete runner
  const handleDelete = useCallback(async (name: string) => {
    setActionLoading(true)
    try {
      const resp = await fetch(`/api/runners/${encodeURIComponent(name)}`, { method: 'DELETE' })
      const data = await resp.json()
      if (data.ok) {
        setRunners(prev => prev.filter(r => r.name !== name))
        if (activeRunner === name) setActiveRunner(null)
      }
    } catch {}
    setActionLoading(false)
    setDeleteConfirm(null)
    setMenuOpen(null)
  }, [activeRunner])

  // Create runner
  const handleCreate = useCallback(async () => {
    if (!formName.trim()) return
    setActionLoading(true)
    try {
      const body: Record<string, string> = {
        name: formName.trim(),
        mode: formMode,
      }
      if (formMode === 'docker' && formDockerImage.trim()) {
        body.docker_image = formDockerImage.trim()
      }
      if (formWorkspace.trim()) {
        body.workspace = formWorkspace.trim()
      }
      const resp = await fetch('/api/runners', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
      })
      const data = await resp.json()
      if (data.ok) {
        // Re-fetch the full runner list to get accurate info (online status, workspace, etc.)
        await fetchRunners()
        setShowAddForm(false)
        setFormName('')
        setFormMode('native')
        setFormDockerImage('ubuntu:22.04')
        setFormWorkspace('')
      }
    } catch {}
    setActionLoading(false)
  }, [formName, formMode, formDockerImage, formWorkspace, fetchRunners])

  // Format mode label
  const modeLabel = (mode: string) => {
    switch (mode) {
      case 'docker': return '🐳 Docker'
      default: return '🖥️ 本地'
    }
  }

  // Shorten workspace path
  const shortPath = (ws: string) => {
    if (!ws) return ''
    const home = ws.replace(/^\/home\/\w+|^\/Users\/\w+/, '~')
    return home
  }

  if (loading) {
    return (
      <div className="settings-section">
        <div className="settings-section-title">🖥️ 工作环境</div>
        <div className="text-center py-6 text-slate-500 text-sm">加载中...</div>
      </div>
    )
  }

  return (
    <div className="settings-section">
      <div className="settings-section-title">🖥️ 工作环境</div>
      <p className="text-xs text-slate-500 mb-3">
        管理远程 Runner，点击卡片切换活跃环境。
      </p>

      {/* Runner cards */}
      {runners.length === 0 && !showAddForm ? (
        <div className="text-center py-6 text-slate-500">
          <p className="text-2xl mb-2">🖥️</p>
          <p className="text-sm">尚未添加工作环境</p>
          <p className="text-xs text-slate-600 mt-1">添加 Runner 后可远程执行命令</p>
        </div>
      ) : (
        <div className="runner-list">
          {runners.map(runner => (
            <div
              key={runner.name}
              className={`runner-card ${activeRunner === runner.name ? 'runner-card-active' : ''} ${runner.online ? 'runner-card-online' : ''}`}
              onClick={() => {
                if (runner.online && activeRunner !== runner.name) {
                  handleSetActive(runner.name)
                }
              }}
            >
              {/* Status indicator + Name + Active badge */}
              <div className="runner-card-header">
                <div className="runner-card-title">
                  <span className={`runner-status-dot ${runner.online ? 'runner-dot-online' : 'runner-dot-offline'}`} />
                  <span className="runner-name">{runner.name}</span>
                  {activeRunner === runner.name && (
                    <span className="runner-active-badge">活跃</span>
                  )}
                </div>
                <div className="runner-card-menu-wrap" ref={menuRef}>
                  <button
                    className="runner-menu-btn"
                    onClick={(e) => {
                      e.stopPropagation()
                      setMenuOpen(menuOpen === runner.name ? null : runner.name)
                    }}
                  >
                    ⋯
                  </button>
                  {menuOpen === runner.name && (
                    <div className="runner-menu" onClick={e => e.stopPropagation()}>
                      <button
                        className="runner-menu-item"
                        onClick={() => {
                          handleCopyCommand(runner)
                          setMenuOpen(null)
                        }}
                      >
                        📋 {copied === runner.name ? '已复制!' : '复制连接命令'}
                      </button>
                      <button
                        className="runner-menu-item runner-menu-item-danger"
                        onClick={() => {
                          setDeleteConfirm(runner.name)
                          setMenuOpen(null)
                        }}
                      >
                        🗑️ 删除
                      </button>
                    </div>
                  )}
                </div>
              </div>

              {/* Info line */}
              <div className="runner-card-info">
                <span>{modeLabel(runner.mode)}</span>
                {runner.docker_image && (
                  <span className="runner-card-meta">· {runner.docker_image}</span>
                )}
                {runner.workspace && (
                  <span className="runner-card-meta">· {shortPath(runner.workspace)}</span>
                )}
              </div>

              {/* Connect command (shown for active or expanded) */}
              {(activeRunner === runner.name || copied === runner.name) && (
                <div className="runner-card-command">
                  <code className="runner-command-text">{buildCommand(runner)}</code>
                  <button
                    className="settings-copy-btn"
                    onClick={(e) => {
                      e.stopPropagation()
                      handleCopyCommand(runner)
                    }}
                    title="复制"
                  >📋</button>
                </div>
              )}
            </div>
          ))}
        </div>
      )}

      {/* Add form */}
      {showAddForm ? (
        <div className="runner-add-form">
          <div className="settings-item">
            <label className="settings-label">名称 *</label>
            <input
              type="text"
              className="settings-input"
              placeholder="例如：MacBook Pro"
              maxLength={50}
              value={formName}
              onChange={e => setFormName(e.target.value)}
              onKeyDown={e => { if (e.key === 'Enter') handleCreate() }}
              autoFocus
            />
          </div>
          <div className="settings-item">
            <label className="settings-label">运行模式</label>
            <div className="flex gap-2 mt-1">
              {[
                { value: 'native' as const, label: '🖥️ 原生' },
                { value: 'docker' as const, label: '🐳 Docker' },
              ].map(opt => (
                <button
                  key={opt.value}
                  className={`flex-1 px-3 py-2 rounded-lg text-sm border transition-colors ${
                    formMode === opt.value
                      ? 'bg-blue-500/20 border-blue-500/50 text-blue-400'
                      : 'bg-slate-800 border-slate-700 text-slate-400 hover:border-slate-500'
                  }`}
                  onClick={() => setFormMode(opt.value)}
                >
                  {opt.label}
                </button>
              ))}
            </div>
          </div>
          {formMode === 'docker' && (
            <div className="settings-item">
              <label className="settings-label">Docker 镜像</label>
              <input
                type="text"
                className="settings-input"
                placeholder="ubuntu:22.04"
                value={formDockerImage}
                onChange={e => setFormDockerImage(e.target.value)}
              />
            </div>
          )}
          <div className="settings-item">
            <label className="settings-label">工作目录</label>
            <input
              type="text"
              className="settings-input"
              placeholder="例如：/home/user/project（留空则由 Runner 自动设定）"
              value={formWorkspace}
              onChange={e => setFormWorkspace(e.target.value)}
            />
            <span className="text-xs text-slate-500 mt-1 block">Runner 连接后将使用此目录作为工作区</span>
          </div>
          <div className="flex gap-2 mt-3">
            <button
              className="settings-action-btn"
              onClick={handleCreate}
              disabled={!formName.trim() || actionLoading}
            >
              {actionLoading ? '⏳ 创建中...' : '✨ 创建'}
            </button>
            <button
              className="settings-action-btn"
              onClick={() => { setShowAddForm(false); setFormName('') }}
            >
              取消
            </button>
          </div>
        </div>
      ) : (
        <button
          className="settings-action-btn w-full mt-3"
          onClick={() => setShowAddForm(true)}
        >
          ➕ 添加工作环境
        </button>
      )}

      {/* Delete confirmation dialog */}
      {deleteConfirm && (
        <>
          <div className="runner-delete-backdrop" onClick={() => setDeleteConfirm(null)} />
          <div className="runner-delete-dialog">
            <div className="runner-delete-title">确认删除</div>
            <p className="runner-delete-text">
              确定要删除 <strong>{deleteConfirm}</strong> 吗？
            </p>
            {runners.find(r => r.name === deleteConfirm)?.online && (
              <p className="runner-delete-warning">
                ⚠️ 此 Runner 当前在线，删除后将断开连接。
              </p>
            )}
            <div className="flex gap-2 mt-4 justify-end">
              <button
                className="settings-action-btn"
                onClick={() => setDeleteConfirm(null)}
                disabled={actionLoading}
              >
                取消
              </button>
              <button
                className="settings-action-btn settings-action-danger"
                onClick={() => handleDelete(deleteConfirm)}
                disabled={actionLoading}
              >
                {actionLoading ? '⏳' : '🗑️ 删除'}
              </button>
            </div>
          </div>
        </>
      )}
    </div>
  )
}
