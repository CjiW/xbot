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

export default function SettingsPanel({ open, onClose, onNicknameChange }: SettingsPanelProps) {
  const [theme, setTheme] = useState<Theme>(() => lsGet('theme', DEFAULT_SETTINGS.theme))
  const [fontSize, setFontSize] = useState<FontSize>(() => lsGet('font_size', DEFAULT_SETTINGS.font_size))
  const [nickname, setNickname] = useState<string>(() => lsGet('nickname', DEFAULT_SETTINGS.nickname))
  const [language, setLanguage] = useState<Language>(() => lsGet('language', DEFAULT_SETTINGS.language))
  const [loaded, setLoaded] = useState(false)
  const [saving, setSaving] = useState(false)

  // Load settings from server on mount
  useEffect(() => {
    if (!open) return
    fetchSettings().then((s) => {
      setTheme(s.theme)
      setFontSize(s.font_size)
      setNickname(s.nickname)
      setLanguage(s.language)
      setLoaded(true)
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
      <div className="settings-panel">
        <div className="flex items-center justify-between mb-6">
          <h2 className="text-lg font-bold text-white">⚙️ 设置</h2>
          {saving && <span className="text-xs text-slate-500">保存中...</span>}
        </div>

        {/* ── 外观设置 ── */}
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
        </div>

        {/* ── 个人信息 ── */}
        <div className={sectionClass}>
          <div className={sectionTitleClass}>👤 个人 Profile</div>

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
        </div>

        {/* ── 通用 ── */}
        <div className={sectionClass}>
          <div className={sectionTitleClass}>🌐 通用 General</div>

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

        <button className="settings-close-btn" onClick={onClose}>
          关闭 Close
        </button>
      </div>
    </>
  )
}
