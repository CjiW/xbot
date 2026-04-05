import { useEffect, useRef, useId, useState } from 'react'

let mermaidModule: typeof import('mermaid').default | null = null
let mermaidLoadPromise: Promise<typeof import('mermaid').default> | null = null
let lastMermaidTheme: string | null = null

async function getMermaid(): Promise<typeof import('mermaid').default> {
  if (!mermaidLoadPromise) {
    mermaidLoadPromise = import('mermaid').then((mod) => {
      mermaidModule = mod.default
      return mermaidModule
    })
  }
  const mod = await mermaidLoadPromise
  const theme = document.documentElement.getAttribute('data-theme') === 'light' ? 'default' : 'dark'
  if (theme !== lastMermaidTheme) {
    mod.initialize({
      startOnLoad: false,
      theme: theme as 'dark' | 'default',
      securityLevel: 'strict',
      fontFamily: 'ui-sans-serif, system-ui, sans-serif',
      themeVariables: theme === 'dark' ? {
        primaryColor: '#6366f1',
        primaryTextColor: '#e2e8f0',
        primaryBorderColor: '#4f46e5',
        lineColor: '#64748b',
        secondaryColor: '#1e293b',
        tertiaryColor: '#0f172a',
        background: '#1e293b',
        mainBkg: '#1e293b',
        nodeBorder: '#4f46e5',
        clusterBkg: '#1e293b80',
        titleColor: '#e2e8f0',
        edgeLabelBackground: '#1e293b',
      } : {
        primaryColor: '#6366f1',
        primaryTextColor: '#1c1917',
        primaryBorderColor: '#4f46e5',
        lineColor: '#78716c',
        secondaryColor: '#e2dfda',
        tertiaryColor: '#f7f5f2',
        background: '#f7f5f2',
        mainBkg: '#f7f5f2',
        nodeBorder: '#4f46e5',
        clusterBkg: '#f7f5f280',
        titleColor: '#1c1917',
        edgeLabelBackground: '#f7f5f2',
      },
    })
    lastMermaidTheme = theme
  }
  return mod
}

/** Returns the current theme key ('light' or 'dark') from the DOM. */
function useTheme() {
  const [theme, setTheme] = useState(
    () => document.documentElement.getAttribute('data-theme') || 'dark'
  )
  useEffect(() => {
    const observer = new MutationObserver(() => {
      setTheme(document.documentElement.getAttribute('data-theme') || 'dark')
    })
    observer.observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme'] })
    return () => observer.disconnect()
  }, [])
  return theme
}

export function MermaidBlock({ code }: { code: string }) {
  const containerRef = useRef<HTMLDivElement>(null)
  const uniqueId = useId().replace(/:/g, '_')
  const [error, setError] = useState<string | null>(null)
  const [svg, setSvg] = useState<string>('')
  const theme = useTheme()

  useEffect(() => {
    let cancelled = false
    const id = `mermaid_${uniqueId}`

    getMermaid()
      .then((mermaid) => mermaid.render(id, code.trim()))
      .then(({ svg }) => {
        if (!cancelled) setSvg(svg)
      })
      .catch((err) => {
        if (!cancelled) {
          // mermaid sometimes throws on invalid syntax
          const msg = err?.message || String(err)
          setError(msg.length > 200 ? msg.slice(0, 200) + '...' : msg)
        }
      })

    return () => {
      cancelled = true
      // Clean up all temporary elements mermaid creates (SVG + defs)
      const ids = [id, `${id}-d`]
      ids.forEach((eid) => {
        const el = document.getElementById(eid)
        if (el) el.remove()
      })
    }
  }, [code, uniqueId, theme])

  if (error) {
    return (
      <div className="rounded-lg bg-red-900/20 border border-red-800/40 p-3 text-sm text-red-400">
        <div className="font-semibold mb-1">Mermaid 渲染失败</div>
        <pre className="text-xs overflow-x-auto whitespace-pre-wrap">{error}</pre>
      </div>
    )
  }

  if (!svg) {
    return (
      <div className="mermaid-wrapper animate-pulse">
        <div className="text-sm text-slate-400">渲染图表中...</div>
      </div>
    )
  }

  return (
    <div
      className="mermaid-wrapper"
      ref={containerRef}
      dangerouslySetInnerHTML={{ __html: svg }}
    />
  )
}
