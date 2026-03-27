import { useEffect, useState } from 'react'

interface SettingsPanelProps {
  open: boolean
  onClose: () => void
}

type Theme = 'dark' | 'light'
type FontSize = 'small' | 'medium' | 'large'

const FONT_SIZE_MAP: Record<FontSize, string> = {
  small: '14px',
  medium: '16px',
  large: '18px',
}

export default function SettingsPanel({ open, onClose }: SettingsPanelProps) {
  const [theme, setTheme] = useState<Theme>(() => {
    return (localStorage.getItem('xbot-theme') as Theme) || 'dark'
  })
  const [fontSize, setFontSize] = useState<FontSize>(() => {
    return (localStorage.getItem('xbot-font-size') as FontSize) || 'medium'
  })

  useEffect(() => {
    document.documentElement.setAttribute('data-theme', theme)
    localStorage.setItem('xbot-theme', theme)
  }, [theme])

  useEffect(() => {
    document.documentElement.style.setProperty('--xbot-font-size', FONT_SIZE_MAP[fontSize])
    localStorage.setItem('xbot-font-size', fontSize)
  }, [fontSize])

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

  return (
    <>
      {/* Backdrop */}
      <div
        className="settings-backdrop"
        onClick={onClose}
      />
      {/* Panel */}
      <div className="settings-panel">
        <h2 className="text-lg font-bold text-white mb-6">⚙️ Settings</h2>

        {/* Theme */}
        <div className="settings-item">
          <label className="settings-label">主题 / Theme</label>
          <select
            className="settings-select"
            value={theme}
            onChange={(e) => setTheme(e.target.value as Theme)}
          >
            <option value="dark">深色 Dark</option>
            <option value="light">浅色 Light</option>
          </select>
        </div>

        {/* Font Size */}
        <div className="settings-item">
          <label className="settings-label">字体大小 / Font Size</label>
          <select
            className="settings-select"
            value={fontSize}
            onChange={(e) => setFontSize(e.target.value as FontSize)}
          >
            <option value="small">小 Small</option>
            <option value="medium">中 Medium</option>
            <option value="large">大 Large</option>
          </select>
        </div>

        <button className="settings-close-btn" onClick={onClose}>
          关闭
        </button>
      </div>
    </>
  )
}
