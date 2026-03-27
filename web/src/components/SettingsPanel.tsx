import { useEffect, useState, useCallback } from 'react'

interface SettingsPanelProps {
  open: boolean
  onClose: () => void
  onNicknameChange?: (nickname: string) => void
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
  // LLM settings
  llm_model: string
  llm_api_key: string
  llm_base_url: string
  // Runner settings
  runner_token: string
}

const DEFAULT_SETTINGS: UserSettings = {
  theme: 'dark',
  font_size: 'medium',
  nickname: '',
  language: 'zh-CN',
  llm_model: '',
  llm_api_key: '',
  llm_base_url: '',
  runner_token: '',
}

// localStorage fallback keys
const LS_KEYS: Record<string, string> = {
  theme: 'xbot-theme',
  font_size: 'xbot-font-size',
  nickname: 'xbot-nickname',
  language: 'xbot-language',
  llm_model: 'xbot-llm-model',
  llm_base_url: 'xbot-llm-base-url',
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
        llm_model: data.settings.llm_model || '',
        llm_api_key: data.settings.llm_api_key || '',
        llm_base_url: data.settings.llm_base_url || '',
        runner_token: data.settings.runner_token || '',
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
    llm_model: lsGet('llm_model', DEFAULT_SETTINGS.llm_model),
    llm_api_key: DEFAULT_SETTINGS.llm_api_key,
    llm_base_url: lsGet('llm_base_url', DEFAULT_SETTINGS.llm_base_url),
    runner_token: DEFAULT_SETTINGS.runner_token,
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

type TabId = 'appearance' | 'llm' | 'runner' | 'market'

const TABS: { id: TabId; label: string; icon: string }[] = [
  { id: 'appearance', label: '外观', icon: '🎨' },
  { id: 'llm', label: 'LLM', icon: '🧠' },
  { id: 'runner', label: 'Runner', icon: '🖥️' },
  { id: 'market', label: '市场', icon: '🏪' },
]

export default function SettingsPanel({ open, onClose, onNicknameChange }: SettingsPanelProps) {
  const [activeTab, setActiveTab] = useState<TabId>('appearance')
  const [theme, setTheme] = useState<Theme>(() => lsGet('theme', DEFAULT_SETTINGS.theme))
  const [fontSize, setFontSize] = useState<FontSize>(() => lsGet('font_size', DEFAULT_SETTINGS.font_size))
  const [nickname, setNickname] = useState<string>(() => lsGet('nickname', DEFAULT_SETTINGS.nickname))
  const [language, setLanguage] = useState<Language>(() => lsGet('language', DEFAULT_SETTINGS.language))
  const [llmModel, setLlmModel] = useState(DEFAULT_SETTINGS.llm_model)
  const [llmApiKey, setLlmApiKey] = useState(DEFAULT_SETTINGS.llm_api_key)
  const [llmBaseUrl, setLlmBaseUrl] = useState(DEFAULT_SETTINGS.llm_base_url)
  const [runnerToken, setRunnerToken] = useState(DEFAULT_SETTINGS.runner_token)
  const [saving, setSaving] = useState(false)

  // Load settings from server on mount
  useEffect(() => {
    if (!open) return
    fetchSettings().then((s) => {
      setTheme(s.theme)
      setFontSize(s.font_size)
      setNickname(s.nickname)
      setLanguage(s.language)
      setLlmModel(s.llm_model)
      setLlmBaseUrl(s.llm_base_url)
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

  const handleSave = useCallback(async (updates: Partial<UserSettings>) => {
    setSaving(true)
    await saveSettings(updates)
    setSaving(false)
  }, [])

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

        {/* ── LLM 设置 ── */}
        {activeTab === 'llm' && (
          <div className={sectionClass}>
            <div className={sectionTitleClass}>🧠 个人 LLM Personal LLM</div>
            <p className="text-xs text-slate-500 mb-3">
              配置个人 LLM 服务。设置后，你的请求将使用你自己的模型而非默认配置。
            </p>

            <div className="settings-item">
              <label className="settings-label">模型 Model</label>
              <input
                type="text"
                className="settings-input"
                placeholder="例如: gpt-4o, claude-sonnet-4-20250514"
                value={llmModel}
                onChange={(e) => setLlmModel(e.target.value)}
                onBlur={() => {
                  lsSet('llm_model', llmModel)
                  handleSave({ llm_model: llmModel, llm_base_url: llmBaseUrl })
                }}
                onKeyDown={(e) => {
                  if (e.key === 'Enter') (e.target as HTMLInputElement).blur()
                }}
              />
            </div>

            <div className="settings-item">
              <label className="settings-label">API Base URL</label>
              <input
                type="text"
                className="settings-input"
                placeholder="例如: https://api.openai.com/v1"
                value={llmBaseUrl}
                onChange={(e) => setLlmBaseUrl(e.target.value)}
                onBlur={() => {
                  lsSet('llm_base_url', llmBaseUrl)
                  handleSave({ llm_model: llmModel, llm_base_url: llmBaseUrl })
                }}
                onKeyDown={(e) => {
                  if (e.key === 'Enter') (e.target as HTMLInputElement).blur()
                }}
              />
            </div>

            <div className="settings-item">
              <label className="settings-label">API Key</label>
              <input
                type="password"
                className="settings-input"
                placeholder="sk-..."
                value={llmApiKey}
                onChange={(e) => setLlmApiKey(e.target.value)}
                onBlur={() => handleSave({ llm_api_key: llmApiKey })}
                onKeyDown={(e) => {
                  if (e.key === 'Enter') (e.target as HTMLInputElement).blur()
                }}
              />
              <p className="text-xs text-slate-600 mt-1">⚠️ API Key 仅存储在服务端，不会返回到前端</p>
            </div>
          </div>
        )}

        {/* ── Runner 设置 ── */}
        {activeTab === 'runner' && (
          <div className={sectionClass}>
            <div className={sectionTitleClass}>🖥️ Remote Runner</div>
            <p className="text-xs text-slate-500 mb-3">
              配置远程 Runner 的认证 Token。设置后，工具将在远程沙箱中执行。
            </p>

            <div className="settings-item">
              <label className="settings-label">Runner Token</label>
              <input
                type="password"
                className="settings-input"
                placeholder="输入 Runner Token..."
                value={runnerToken}
                onChange={(e) => setRunnerToken(e.target.value)}
                onBlur={() => handleSave({ runner_token: runnerToken })}
                onKeyDown={(e) => {
                  if (e.key === 'Enter') (e.target as HTMLInputElement).blur()
                }}
              />
              <p className="text-xs text-slate-600 mt-1">⚠️ Token 仅存储在服务端</p>
            </div>
          </div>
        )}

        {/* ── Agent 市场 ── */}
        {activeTab === 'market' && (
          <div className={sectionClass}>
            <div className={sectionTitleClass}>🏪 Agent 市场 Agent Market</div>
            <div className="text-center py-8 text-slate-500">
              <p className="text-3xl mb-3">🏗️</p>
              <p className="text-sm">Agent 市场正在建设中...</p>
              <p className="text-xs mt-1 text-slate-600">
                即将支持浏览、安装和管理自定义 Agent 角色
              </p>
            </div>
          </div>
        )}
      </div>
    </>
  )
}
