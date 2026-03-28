import { useEffect, useState, useCallback } from 'react'

import type { PresetCommand } from '../types'

interface SettingsPanelProps {
  open: boolean
  onClose: () => void
  onNicknameChange?: (nickname: string) => void
  onPresetsChange?: (presets: PresetCommand[]) => void
}

type Theme = 'dark' | 'light'
type FontSize = 'small' | 'medium' | 'large'
type Language = 'zh-CN' | 'en'

const FONT_SIZE_MAP: Record<FontSize, string> = {
  small: '14px',
  medium: '16px',
  large: '18px',
}

interface UserSettings {
  theme: Theme
  font_size: FontSize
  nickname: string
  language: Language
  preset_commands?: string
}

const DEFAULT_SETTINGS: UserSettings = {
  theme: 'dark',
  font_size: 'medium',
  nickname: '',
  language: 'zh-CN',
}

// localStorage fallback keys
const LS_KEYS: Record<string, string> = {
  theme: 'xbot-theme',
  font_size: 'xbot-font-size',
  nickname: 'xbot-nickname',
  language: 'xbot-language',
}

function lsGet<K extends keyof UserSettings>(key: K, fallback: UserSettings[K]): UserSettings[K] {
  const raw = localStorage.getItem(LS_KEYS[key])
  return (raw as UserSettings[K]) || fallback
}

function lsSet<K extends keyof UserSettings>(key: K, value: UserSettings[K]) {
  localStorage.setItem(LS_KEYS[key], value as string)
}

async function fetchSettings(): Promise<UserSettings> {
  try {
    const resp = await fetch('/api/settings')
    const data = await resp.json()
    if (data.ok && data.settings) {
      return {
        theme: (data.settings.theme as Theme) || lsGet('theme', DEFAULT_SETTINGS.theme),
        font_size: (data.settings.font_size as FontSize) || lsGet('font_size', DEFAULT_SETTINGS.font_size),
        nickname: data.settings.nickname || lsGet('nickname', DEFAULT_SETTINGS.nickname),
        language: (data.settings.language as Language) || lsGet('language', DEFAULT_SETTINGS.language),
        preset_commands: data.settings.preset_commands,
      }
    }
  } catch {
    // Server unreachable — use localStorage fallback
  }
  return {
    theme: lsGet('theme', DEFAULT_SETTINGS.theme),
    font_size: lsGet('font_size', DEFAULT_SETTINGS.font_size),
    nickname: lsGet('nickname', DEFAULT_SETTINGS.nickname),
    language: lsGet('language', DEFAULT_SETTINGS.language),
  }
}

async function saveSettings(settings: Partial<UserSettings>): Promise<boolean> {
  try {
    const resp = await fetch('/api/settings', {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ settings }),
    })
    const data = await resp.json()
    return data.ok === true
  } catch {
    return false
  }
}

type TabId = 'appearance' | 'presets' | 'llm' | 'runner' | 'market'

interface MarketEntry {
  id: number
  type: string
  name: string
  description: string
  author: string
  created_at: string
  installed: boolean
}

interface MyMarketEntry {
  name: string
  type: string
  description: string
  published: boolean
}

const TABS: { id: TabId; label: string; icon: string }[] = [
  { id: 'appearance', label: '外观', icon: '🎨' },
  { id: 'presets', label: '快捷指令', icon: '⚡' },
  { id: 'llm', label: 'LLM', icon: '🧠' },
  { id: 'runner', label: 'Runner', icon: '🖥️' },
  { id: 'market', label: '市场', icon: '🏪' },
]

// ── LLM Config types ──

interface LLMConfig {
  provider: string
  base_url: string
  model: string
  models: string[]
}

const PROVIDER_OPTIONS = [
  { value: 'openai', label: 'OpenAI (GPT / o-series)' },
  { value: 'anthropic', label: 'Anthropic (Claude)' },
]

export default function SettingsPanel({ open, onClose, onNicknameChange, onPresetsChange }: SettingsPanelProps) {
  const [activeTab, setActiveTab] = useState<TabId>('appearance')
  const [theme, setTheme] = useState<Theme>(() => lsGet('theme', DEFAULT_SETTINGS.theme))
  const [fontSize, setFontSize] = useState<FontSize>(() => lsGet('font_size', DEFAULT_SETTINGS.font_size))
  const [nickname, setNickname] = useState<string>(() => lsGet('nickname', DEFAULT_SETTINGS.nickname))
  const [language, setLanguage] = useState<Language>(() => lsGet('language', DEFAULT_SETTINGS.language))
  const [runnerCommand, setRunnerCommand] = useState('')
  const [tokenActionloading, setTokenActionLoading] = useState(false)
  const [saving, setSaving] = useState(false)
  const [marketType, setMarketType] = useState<'agent' | 'skill'>('agent')
  const [marketSubTab, setMarketSubTab] = useState<'browse' | 'mine'>('browse')
  const [marketEntries, setMarketEntries] = useState<MarketEntry[]>([])
  const [myMarketEntries, setMyMarketEntries] = useState<MyMarketEntry[]>([])
  const [marketLoading, setMarketLoading] = useState(false)

  // Preset commands state
  const [presetList, setPresetList] = useState<PresetCommand[]>([])
  const [editingPreset, setEditingPreset] = useState<PresetCommand | null>(null)
  const [presetSaving, setPresetSaving] = useState(false)

  // LLM config state
  const [llmConfig, setLlmConfig] = useState<LLMConfig | null>(null)
  const [llmConfigLoading, setLlmConfigLoading] = useState(false)
  const [llmSaving, setLlmSaving] = useState(false)
  // Add form state (for new config)
  const [llmFormProvider, setLlmFormProvider] = useState('openai')
  const [llmFormBaseUrl, setLlmFormBaseUrl] = useState('')
  const [llmFormApiKey, setLlmFormApiKey] = useState('')
  const [llmFormModel, setLlmFormModel] = useState('')
  const [llmError, setLlmError] = useState('')

  // Load settings from server on mount
  useEffect(() => {
    if (!open) return
    fetchSettings().then((s) => {
      setTheme(s.theme)
      setFontSize(s.font_size)
      setNickname(s.nickname)
      setLanguage(s.language)
      // Load presets from the same response
      if (s.preset_commands) {
        try {
          const parsed = JSON.parse(s.preset_commands)
          if (Array.isArray(parsed)) setPresetList(parsed)
        } catch { /* ignore */ }
      }
    })
  }, [open])

  // Apply theme
  useEffect(() => {
    document.documentElement.setAttribute('data-theme', theme)
    lsSet('theme', theme)
  }, [theme])

  // Apply font size
  useEffect(() => {
    document.documentElement.style.setProperty('--xbot-font-size', FONT_SIZE_MAP[fontSize])
    lsSet('font_size', fontSize)
  }, [fontSize])

  // Persist nickname locally
  useEffect(() => {
    lsSet('nickname', nickname)
  }, [nickname])

  // Persist language locally
  useEffect(() => {
    lsSet('language', language)
  }, [language])

  // Fetch runner token command when runner tab is opened
  useEffect(() => {
    if (!open || activeTab !== 'runner') return
    setTokenActionLoading(true)
    fetch('/api/runner/token')
      .then(r => r.json())
      .then(data => {
        if (data.ok) setRunnerCommand(data.command || '')
      })
      .catch(() => {})
      .finally(() => setTokenActionLoading(false))
  }, [open, activeTab])

  // Fetch LLM config when tab is opened
  const fetchLLMConfig = useCallback(async () => {
    setLlmConfigLoading(true)
    setLlmError('')
    try {
      const resp = await fetch('/api/llm-config')
      const data = await resp.json()
      if (data.ok && data.provider) {
        setLlmConfig({
          provider: data.provider,
          base_url: data.base_url,
          model: data.model,
          models: data.models || [],
        })
      } else {
        setLlmConfig(null)
      }
    } catch {
      setLlmError('获取配置失败')
    }
    setLlmConfigLoading(false)
  }, [])

  useEffect(() => {
    if (open && activeTab === 'llm') fetchLLMConfig()
  }, [open, activeTab, fetchLLMConfig])

  const handleGenerateToken = useCallback(async () => {
    setTokenActionLoading(true)
    try {
      const resp = await fetch('/api/runner/token', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ mode: 'native', docker_image: '', workspace: '' }),
      })
      const data = await resp.json()
      if (data.ok) setRunnerCommand(data.command || '')
    } catch {}
    setTokenActionLoading(false)
  }, [])

  const handleRegenerateToken = useCallback(async () => {
    setTokenActionLoading(true)
    try {
      const resp = await fetch('/api/runner/token', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ mode: 'native', docker_image: '', workspace: '' }),
      })
      const data = await resp.json()
      if (data.ok) setRunnerCommand(data.command || '')
    } catch {}
    setTokenActionLoading(false)
  }, [])

  const handleRevokeToken = useCallback(async () => {
    setTokenActionLoading(true)
    try {
      const resp = await fetch('/api/runner/token', { method: 'DELETE' })
      const data = await resp.json()
      if (data.ok) setRunnerCommand('')
    } catch {}
    setTokenActionLoading(false)
  }, [])

  const handleSave = useCallback(async (updates: Partial<UserSettings>) => {
    setSaving(true)
    await saveSettings(updates)
    setSaving(false)
  }, [])

  // Preset commands CRUD
  const savePresets = useCallback(async (list: PresetCommand[]) => {
    setPresetSaving(true)
    const sorted = [...list].sort((a, b) => a.sort - b.sort)
    const ok = await saveSettings({ preset_commands: JSON.stringify(sorted) })
    if (ok) {
      setPresetList(sorted)
      onPresetsChange?.(sorted)
    }
    setPresetSaving(false)
    return ok
  }, [onPresetsChange])

  const handlePresetAdd = useCallback(() => {
    setEditingPreset({
      id: Math.random().toString(36).slice(2, 10) + Date.now().toString(36),
      label: '',
      icon: '⚡',
      content: '',
      fill: false,
      sort: presetList.length,
    })
  }, [presetList.length])

  const handlePresetSave = useCallback(async (preset: PresetCommand) => {
    const exists = presetList.find(p => p.id === preset.id)
    const newList = exists
      ? presetList.map(p => p.id === preset.id ? preset : p)
      : [...presetList, preset]
    const ok = await savePresets(newList)
    if (ok) setEditingPreset(null)
  }, [presetList, savePresets])

  const handlePresetDelete = useCallback(async (id: string) => {
    if (!confirm('确认删除此快捷指令？')) return
    const newList = presetList
      .filter(p => p.id !== id)
      .map((p, i) => ({ ...p, sort: i }))
    await savePresets(newList)
  }, [presetList, savePresets])

  const handlePresetMove = useCallback(async (id: string, direction: 'up' | 'down') => {
    const sorted = [...presetList].sort((a, b) => a.sort - b.sort)
    const idx = sorted.findIndex(p => p.id === id)
    if (idx < 0) return
    const swapIdx = direction === 'up' ? idx - 1 : idx + 1
    if (swapIdx < 0 || swapIdx >= sorted.length) return
    const temp = sorted[idx].sort
    sorted[idx] = { ...sorted[idx], sort: sorted[swapIdx].sort }
    sorted[swapIdx] = { ...sorted[swapIdx], sort: temp }
    await savePresets(sorted)
  }, [presetList, savePresets])

  // LLM config actions
  const handleLLMAdd = useCallback(async () => {
    if (!llmFormBaseUrl.trim() || !llmFormApiKey.trim()) {
      setLlmError('Base URL 和 API Key 为必填项')
      return
    }
    setLlmSaving(true)
    setLlmError('')
    try {
      const resp = await fetch('/api/llm-config', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          provider: llmFormProvider,
          base_url: llmFormBaseUrl.trim(),
          api_key: llmFormApiKey.trim(),
          model: llmFormModel.trim(),
        }),
      })
      const data = await resp.json()
      if (data.ok) {
        setLlmFormBaseUrl('')
        setLlmFormApiKey('')
        setLlmFormModel('')
        await fetchLLMConfig()
      } else {
        setLlmError(data.error || '保存失败')
      }
    } catch {
      setLlmError('网络错误')
    }
    setLlmSaving(false)
  }, [llmFormProvider, llmFormBaseUrl, llmFormApiKey, llmFormModel, fetchLLMConfig])

  const handleLLMSetModel = useCallback(async (model: string) => {
    setLlmSaving(true)
    setLlmError('')
    try {
      const resp = await fetch('/api/llm-config/model', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ model }),
      })
      const data = await resp.json()
      if (data.ok) {
        await fetchLLMConfig()
      } else {
        setLlmError(data.error || '切换模型失败')
      }
    } catch {
      setLlmError('网络错误')
    }
    setLlmSaving(false)
  }, [fetchLLMConfig])

  const handleLLMDelete = useCallback(async () => {
    if (!confirm('确认删除个人 LLM 配置？删除后将恢复使用系统默认模型。')) return
    setLlmSaving(true)
    setLlmError('')
    try {
      const resp = await fetch('/api/llm-config', { method: 'DELETE' })
      const data = await resp.json()
      if (data.ok) {
        setLlmConfig(null)
        // Clear form too
        setLlmFormBaseUrl('')
        setLlmFormApiKey('')
        setLlmFormModel('')
      } else {
        setLlmError(data.error || '删除失败')
      }
    } catch {
      setLlmError('网络错误')
    }
    setLlmSaving(false)
  }, [])

  // Market functions
  const loadMarket = useCallback(async () => {
    setMarketLoading(true)
    try {
      const resp = await fetch(`/api/market?type=${marketType}&limit=50`)
      const data = await resp.json()
      if (data.ok) setMarketEntries(data.entries || [])
    } catch {}
    setMarketLoading(false)
  }, [marketType])

  const handleInstall = useCallback(async (entry: MarketEntry) => {
    try {
      const resp = await fetch('/api/market/install', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ type: entry.type, id: entry.id }),
      })
      const data = await resp.json()
      if (data.ok) loadMarket()
    } catch {}
  }, [loadMarket])

  const handleUninstall = useCallback(async (entry: MarketEntry) => {
    try {
      const resp = await fetch('/api/market/uninstall', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ type: entry.type, name: entry.name }),
      })
      const data = await resp.json()
      if (data.ok) loadMarket()
    } catch {}
  }, [loadMarket])

  const loadMyMarket = useCallback(async () => {
    setMarketLoading(true)
    try {
      const resp = await fetch(`/api/market/my?type=${marketType}`)
      const data = await resp.json()
      if (data.ok) setMyMarketEntries(data.entries || [])
    } catch {}
    setMarketLoading(false)
  }, [marketType])

  const handlePublish = useCallback(async (entry: MyMarketEntry) => {
    try {
      const resp = await fetch('/api/market/publish', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ type: entry.type, name: entry.name }),
      })
      const data = await resp.json()
      if (data.ok) {
        setMyMarketEntries(prev => prev.map(e =>
          e.name === entry.name && e.type === entry.type ? { ...e, published: true } : e
        ))
      }
    } catch {}
  }, [])

  const handleUnpublish = useCallback(async (entry: MyMarketEntry) => {
    try {
      const resp = await fetch('/api/market/unpublish', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ type: entry.type, name: entry.name }),
      })
      const data = await resp.json()
      if (data.ok) {
        setMyMarketEntries(prev => prev.map(e =>
          e.name === entry.name && e.type === entry.type ? { ...e, published: false } : e
        ))
      }
    } catch {}
  }, [])

  // Load market when tab is opened
  useEffect(() => {
    if (open && activeTab === 'market') {
      if (marketSubTab === 'browse') loadMarket()
      else loadMyMarket()
    }
  }, [open, activeTab, marketSubTab, loadMarket, loadMyMarket])

  // Close on Escape
  useEffect(() => {
    if (!open) return
    const handleKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose()
    }
    window.addEventListener('keydown', handleKey)
    return () => window.removeEventListener('keydown', handleKey)
  }, [open, onClose])

  if (!open) return null

  const sectionClass = 'settings-section'
  const sectionTitleClass = 'settings-section-title'

  const providerLabel = PROVIDER_OPTIONS.find(p => p.value === llmConfig?.provider)?.label || llmConfig?.provider

  return (
    <>
      {/* Backdrop */}
      <div
        className="settings-backdrop"
        onClick={onClose}
      />
      {/* Panel */}
      <div className="settings-panel" style={{ maxWidth: '520px' }}>
        <div className="flex items-center justify-between mb-4">
          <h2 className="text-lg font-bold text-white">⚙️ 设置</h2>
          <div className="flex items-center gap-2">
            {saving && <span className="text-xs text-slate-500">保存中...</span>}
            <button className="settings-close-btn text-sm" onClick={onClose}>✕</button>
          </div>
        </div>

        {/* Tabs */}
        <div className="flex gap-1 mb-4 p-1 bg-slate-700/50 rounded-lg">
          {TABS.map((tab) => (
            <button
              key={tab.id}
              onClick={() => setActiveTab(tab.id)}
              className={`flex-1 text-xs py-1.5 px-2 rounded-md transition-colors ${
                activeTab === tab.id
                  ? 'bg-slate-600 text-white'
                  : 'text-slate-400 hover:text-white hover:bg-slate-700'
              }`}
            >
              {tab.icon} {tab.label}
            </button>
          ))}
        </div>

        {/* ── 外观设置 ── */}
        {activeTab === 'appearance' && (
          <div className={sectionClass}>
            <div className={sectionTitleClass}>🎨 外观 Appearance</div>

            <div className="settings-item">
              <label className="settings-label">主题 Theme</label>
              <select
                className="settings-select"
                value={theme}
                onChange={(e) => {
                  const v = e.target.value as Theme
                  setTheme(v)
                  handleSave({ theme: v, font_size: fontSize, nickname, language })
                }}
              >
                <option value="dark">深色 Dark</option>
                <option value="light">浅色 Light</option>
              </select>
            </div>

            <div className="settings-item">
              <label className="settings-label">字体大小 Font Size</label>
              <select
                className="settings-select"
                value={fontSize}
                onChange={(e) => {
                  const v = e.target.value as FontSize
                  setFontSize(v)
                  handleSave({ theme, font_size: v, nickname, language })
                }}
              >
                <option value="small">小 Small</option>
                <option value="medium">中 Medium</option>
                <option value="large">大 Large</option>
              </select>
            </div>

            <div className="settings-item">
              <label className="settings-label">昵称 Nickname</label>
              <input
                type="text"
                className="settings-input"
                placeholder="输入昵称..."
                maxLength={32}
                value={nickname}
                onChange={(e) => setNickname(e.target.value)}
                onBlur={() => {
                  onNicknameChange?.(nickname)
                  handleSave({ theme, font_size: fontSize, nickname, language })
                }}
                onKeyDown={(e) => {
                  if (e.key === 'Enter') {
                    ;(e.target as HTMLInputElement).blur()
                  }
                }}
              />
            </div>

            <div className="settings-item">
              <label className="settings-label">语言 Language</label>
              <select
                className="settings-select"
                value={language}
                onChange={(e) => {
                  const v = e.target.value as Language
                  setLanguage(v)
                  handleSave({ theme, font_size: fontSize, nickname, language: v })
                }}
              >
                <option value="zh-CN">简体中文</option>
                <option value="en">English</option>
              </select>
            </div>
          </div>
        )}

        {/* ── 快捷指令 ── */}
        {activeTab === 'presets' && (
          <div className={sectionClass}>
            <div className={sectionTitleClass}>⚡ 快捷指令 Preset Commands</div>
            <p className="text-xs text-slate-500 mb-3">
              配置常用指令，在聊天输入框上方快速触发。最多 20 条。
            </p>

            {editingPreset ? (
              /* ── 编辑/新增表单 ── */
              <div className="preset-edit-form">
                <div className="settings-item">
                  <label className="settings-label">图标 Icon</label>
                  <input
                    type="text"
                    className="settings-input"
                    style={{ width: '60px' }}
                    maxLength={4}
                    value={editingPreset.icon}
                    onChange={(e) => setEditingPreset({ ...editingPreset, icon: e.target.value })}
                  />
                </div>
                <div className="settings-item">
                  <label className="settings-label">名称 Label *</label>
                  <input
                    type="text"
                    className="settings-input"
                    placeholder="例如：代码审查"
                    maxLength={20}
                    value={editingPreset.label}
                    onChange={(e) => setEditingPreset({ ...editingPreset, label: e.target.value })}
                  />
                </div>
                <div className="settings-item">
                  <label className="settings-label">内容 Content *</label>
                  <textarea
                    className="settings-input"
                    style={{ minHeight: '80px', resize: 'vertical' }}
                    placeholder="点击后发送的内容..."
                    maxLength={2000}
                    value={editingPreset.content}
                    onChange={(e) => setEditingPreset({ ...editingPreset, content: e.target.value })}
                  />
                  <p className="text-xs text-slate-600 mt-1">{editingPreset.content.length}/2000</p>
                </div>
                <div className="settings-item">
                  <label className="settings-label flex items-center gap-2">
                    <input
                      type="checkbox"
                      checked={editingPreset.fill ?? false}
                      onChange={(e) => setEditingPreset({ ...editingPreset, fill: e.target.checked })}
                    />
                    填充模式（填入输入框而非直接发送）
                  </label>
                </div>
                <div className="flex gap-2 mt-3">
                  <button
                    className="settings-action-btn"
                    onClick={() => handlePresetSave(editingPreset)}
                    disabled={!editingPreset.label.trim() || !editingPreset.content.trim() || presetSaving}
                  >
                    {presetSaving ? '保存中...' : '💾 保存'}
                  </button>
                  <button
                    className="settings-action-btn settings-action-danger"
                    onClick={() => setEditingPreset(null)}
                  >
                    取消
                  </button>
                </div>
              </div>
            ) : (
              /* ── 列表视图 ── */
              <>
                {presetList.length === 0 ? (
                  <div className="text-center py-6 text-slate-500">
                    <p className="text-2xl mb-2">📭</p>
                    <p className="text-sm">暂无快捷指令</p>
                  </div>
                ) : (
                  <div className="preset-list">
                    {[...presetList].sort((a, b) => a.sort - b.sort).map((p) => (
                      <div key={p.id} className="preset-item">
                        <div className="preset-item-main">
                          <span className="preset-item-icon">{p.icon || '⚡'}</span>
                          <div className="preset-item-info">
                            <span className="preset-item-label">{p.label}</span>
                            <span className="preset-item-content">{p.content.length > 40 ? p.content.slice(0, 40) + '...' : p.content}</span>
                          </div>
                        </div>
                        <div className="preset-item-actions">
                          <button
                            className="preset-action-btn"
                            onClick={() => handlePresetMove(p.id, 'up')}
                            title="上移"
                            disabled={presetSaving}
                          >↑</button>
                          <button
                            className="preset-action-btn"
                            onClick={() => handlePresetMove(p.id, 'down')}
                            title="下移"
                            disabled={presetSaving}
                          >↓</button>
                          <button
                            className="preset-action-btn"
                            onClick={() => setEditingPreset({ ...p })}
                            title="编辑"
                            disabled={presetSaving}
                          >✏️</button>
                          <button
                            className="preset-action-btn preset-action-delete"
                            onClick={() => handlePresetDelete(p.id)}
                            title="删除"
                            disabled={presetSaving}
                          >🗑️</button>
                        </div>
                      </div>
                    ))}
                  </div>
                )}
                <button
                  className="settings-action-btn w-full mt-3"
                  onClick={handlePresetAdd}
                  disabled={presetList.length >= 20 || presetSaving}
                >
                  ➕ 新增指令 {presetList.length > 0 ? `(${presetList.length}/20)` : ''}
                </button>
              </>
            )}
          </div>
        )}

        {/* ── LLM 设置 ── */}
        {activeTab === 'llm' && (
          <div className={sectionClass}>
            <div className={sectionTitleClass}>🧠 个人 LLM 配置</div>

            {llmConfigLoading ? (
              <div className="text-center py-6 text-slate-500 text-sm">加载中...</div>
            ) : llmConfig ? (
              /* ── 已有配置：显示当前配置 + 模型切换 + 删除 ── */
              <>
                <div className="text-xs text-slate-400 mb-3">
                  当前使用个人模型。可切换模型或删除配置以恢复系统默认。
                </div>

                <div className="settings-item">
                  <label className="settings-label">提供商 Provider</label>
                  <div className="text-sm text-slate-300">{providerLabel}</div>
                </div>

                <div className="settings-item">
                  <label className="settings-label">Base URL</label>
                  <div className="text-sm text-slate-400 font-mono break-all">{llmConfig.base_url}</div>
                </div>

                <div className="settings-item">
                  <label className="settings-label">当前模型 Model</label>
                  {llmConfig.models.length > 0 ? (
                    <select
                      className="settings-select"
                      value={llmConfig.model}
                      onChange={(e) => handleLLMSetModel(e.target.value)}
                      disabled={llmSaving}
                    >
                      {llmConfig.models.map(m => (
                        <option key={m} value={m}>{m}</option>
                      ))}
                    </select>
                  ) : (
                    <div className="text-sm text-slate-300">{llmConfig.model || '默认'}</div>
                  )}
                </div>

                {llmError && <p className="text-xs text-red-400 mt-1 mb-2">{llmError}</p>}

                <div className="flex gap-2 mt-3">
                  <button
                    className="settings-action-btn settings-action-danger"
                    onClick={handleLLMDelete}
                    disabled={llmSaving}
                  >
                    🗑️ 删除配置
                  </button>
                </div>
              </>
            ) : (
              /* ── 无配置：新增表单 ── */
              <>
                <div className="text-xs text-slate-400 mb-3">
                  当前使用系统默认模型。配置个人 LLM 后可自由选择模型。
                </div>

                <div className="settings-item">
                  <label className="settings-label">提供商 Provider</label>
                  <select
                    className="settings-select"
                    value={llmFormProvider}
                    onChange={(e) => setLlmFormProvider(e.target.value)}
                  >
                    {PROVIDER_OPTIONS.map(p => (
                      <option key={p.value} value={p.value}>{p.label}</option>
                    ))}
                  </select>
                </div>

                <div className="settings-item">
                  <label className="settings-label">Base URL *</label>
                  <input
                    type="text"
                    className="settings-input"
                    placeholder="例如: https://api.openai.com/v1"
                    value={llmFormBaseUrl}
                    onChange={(e) => setLlmFormBaseUrl(e.target.value)}
                  />
                </div>

                <div className="settings-item">
                  <label className="settings-label">API Key *</label>
                  <input
                    type="password"
                    className="settings-input"
                    placeholder="sk-..."
                    value={llmFormApiKey}
                    onChange={(e) => setLlmFormApiKey(e.target.value)}
                  />
                  <p className="text-xs text-slate-600 mt-1">⚠️ API Key 仅存储在服务端，不会返回到前端</p>
                </div>

                <div className="settings-item">
                  <label className="settings-label">模型 Model</label>
                  <input
                    type="text"
                    className="settings-input"
                    placeholder="例如: gpt-4o, claude-sonnet-4-20250514（可选，默认用提供商推荐模型）"
                    value={llmFormModel}
                    onChange={(e) => setLlmFormModel(e.target.value)}
                  />
                </div>

                {llmError && <p className="text-xs text-red-400 mt-1 mb-2">{llmError}</p>}

                <button
                  className="settings-action-btn w-full mt-2"
                  onClick={handleLLMAdd}
                  disabled={llmSaving}
                >
                  {llmSaving ? '保存中...' : '💾 保存配置'}
                </button>
              </>
            )}
          </div>
        )}

        {/* ── Runner 设置 ── */}
        {activeTab === 'runner' && (
          <div className={sectionClass}>
            <div className={sectionTitleClass}>🖥️ Remote Runner</div>
            <p className="text-xs text-slate-500 mb-3">
              远程沙箱允许工具命令在你的本地机器或 Docker 容器中执行。
            </p>
            {tokenActionloading ? (
              <div className="text-center py-4 text-slate-500 text-sm">加载中...</div>
            ) : runnerCommand ? (
              <>
                <div className="settings-item">
                  <label className="settings-label">连接命令</label>
                  <div className="relative">
                    <code className="settings-code-block">{runnerCommand}</code>
                    <button
                      className="settings-copy-btn"
                      onClick={() => navigator.clipboard.writeText(runnerCommand)}
                      title="复制"
                    >📋</button>
                  </div>
                </div>
                <div className="flex gap-2 mt-3">
                  <button
                    className="settings-action-btn settings-action-danger"
                    onClick={handleRegenerateToken}
                    disabled={tokenActionloading}
                  >
                    🔄 重新生成
                  </button>
                  <button
                    className="settings-action-btn settings-action-danger"
                    onClick={handleRevokeToken}
                    disabled={tokenActionloading}
                  >
                    🗑️ 撤销 Token
                  </button>
                </div>
              </>
            ) : (
              <div className="text-center py-4">
                <p className="text-slate-400 text-sm">尚未配置远程 Runner</p>
                <button
                  className="settings-action-btn mt-3"
                  onClick={handleGenerateToken}
                  disabled={tokenActionloading}
                >
                  ✨ 生成 Token
                </button>
              </div>
            )}
          </div>
        )}

        {/* ── Agent 市场 ── */}
        {activeTab === 'market' && (
          <div className={sectionClass}>
            <div className={sectionTitleClass}>🏪 Agent 市场</div>
            <div className="market-tab-bar">
              <button
                className={`market-tab ${marketType === 'agent' ? 'active' : ''}`}
                onClick={() => { setMarketType('agent'); setMarketSubTab('browse'); }}
              >
                🤖 Agent
              </button>
              <button
                className={`market-tab ${marketType === 'skill' ? 'active' : ''}`}
                onClick={() => { setMarketType('skill'); setMarketSubTab('browse'); }}
              >
                🛠️ Skill
              </button>
            </div>
            {/* Sub tabs: browse / mine */}
            <div className="market-sub-tab-bar">
              <button
                className={`market-tab market-sub-tab ${marketSubTab === 'browse' ? 'active' : ''}`}
                onClick={() => setMarketSubTab('browse')}
              >
                📦 市场
              </button>
              <button
                className={`market-tab market-sub-tab ${marketSubTab === 'mine' ? 'active' : ''}`}
                onClick={() => setMarketSubTab('mine')}
              >
                📋 我的
              </button>
            </div>

            {marketSubTab === 'browse' && (
              marketLoading ? (
                <div className="text-center py-8 text-slate-500">
                  <div className="market-spinner" />
                  <p className="text-xs mt-2">加载中...</p>
                </div>
              ) : marketEntries.length === 0 ? (
                <div className="text-center py-8 text-slate-500">
                  <p className="text-3xl mb-3">📭</p>
                  <p className="text-sm">暂无可用条目</p>
                </div>
              ) : (
                <div className="market-entry-list">
                  {marketEntries.map(entry => (
                    <div key={entry.id} className="market-entry">
                      <div className="market-entry-header">
                        <div className="market-entry-info">
                          <span className="market-entry-name">{entry.name}</span>
                          <span className="market-entry-author">by {entry.author}</span>
                        </div>
                        {entry.installed ? (
                          <button className="market-uninstall-btn" onClick={() => handleUninstall(entry)}>
                            卸载
                          </button>
                        ) : (
                          <button className="market-install-btn" onClick={() => handleInstall(entry)}>
                            安装
                          </button>
                        )}
                      </div>
                      {entry.description && (
                        <p className="market-entry-desc">{entry.description}</p>
                      )}
                    </div>
                  ))}
                </div>
              )
            )}

            {marketSubTab === 'mine' && (
              marketLoading ? (
                <div className="text-center py-8 text-slate-500">
                  <div className="market-spinner" />
                  <p className="text-xs mt-2">加载中...</p>
                </div>
              ) : myMarketEntries.length === 0 ? (
                <div className="text-center py-8 text-slate-500">
                  <p className="text-3xl mb-3">📭</p>
                  <p className="text-sm">暂无自己的{marketType === 'skill' ? ' Skill' : ' Agent'}</p>
                </div>
              ) : (
                <div className="market-entry-list">
                  {myMarketEntries.map(entry => (
                    <div key={entry.name} className="market-entry">
                      <div className="market-entry-header">
                        <div className="market-entry-info">
                          <span className="market-entry-name">{entry.name}</span>
                          <span className={`market-entry-status ${entry.published ? 'published' : 'unpublished'}`}>
                            {entry.published ? '✅ 已上架' : '⚪ 未上架'}
                          </span>
                        </div>
                        {entry.published ? (
                          <button className="market-unpublish-btn" onClick={() => handleUnpublish(entry)}>
                            下架
                          </button>
                        ) : (
                          <button className="market-install-btn" onClick={() => handlePublish(entry)}>
                            上架
                          </button>
                        )}
                      </div>
                      {entry.description && (
                        <p className="market-entry-desc">{entry.description}</p>
                      )}
                    </div>
                  ))}
                </div>
              )
            )}
          </div>
        )}
      </div>
    </>
  )
}
