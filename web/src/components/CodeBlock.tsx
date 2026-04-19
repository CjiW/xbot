import { useState } from 'react'
import hljs from 'highlight.js/lib/core'
import { MermaidBlock } from './MermaidBlock'
import javascript from 'highlight.js/lib/languages/javascript'
import typescript from 'highlight.js/lib/languages/typescript'
import go from 'highlight.js/lib/languages/go'
import python from 'highlight.js/lib/languages/python'
import bash from 'highlight.js/lib/languages/bash'
import json from 'highlight.js/lib/languages/json'
import yaml from 'highlight.js/lib/languages/yaml'
import css from 'highlight.js/lib/languages/css'
import xml from 'highlight.js/lib/languages/xml'
import markdown from 'highlight.js/lib/languages/markdown'
import sql from 'highlight.js/lib/languages/sql'
import rust from 'highlight.js/lib/languages/rust'
import java from 'highlight.js/lib/languages/java'
import cpp from 'highlight.js/lib/languages/cpp'
import diff from 'highlight.js/lib/languages/diff'
import 'highlight.js/styles/github-dark.css'

hljs.registerLanguage('javascript', javascript)
hljs.registerLanguage('js', javascript)
hljs.registerLanguage('typescript', typescript)
hljs.registerLanguage('ts', typescript)
hljs.registerLanguage('go', go)
hljs.registerLanguage('python', python)
hljs.registerLanguage('py', python)
hljs.registerLanguage('bash', bash)
hljs.registerLanguage('sh', bash)
hljs.registerLanguage('shell', bash)
hljs.registerLanguage('json', json)
hljs.registerLanguage('yaml', yaml)
hljs.registerLanguage('yml', yaml)
hljs.registerLanguage('css', css)
hljs.registerLanguage('html', xml)
hljs.registerLanguage('xml', xml)
hljs.registerLanguage('svg', xml)
hljs.registerLanguage('markdown', markdown)
hljs.registerLanguage('md', markdown)
hljs.registerLanguage('sql', sql)
hljs.registerLanguage('rust', rust)
hljs.registerLanguage('rs', rust)
hljs.registerLanguage('java', java)
hljs.registerLanguage('cpp', cpp)
hljs.registerLanguage('c', cpp)
hljs.registerLanguage('diff', diff)

interface CodeBlockProps {
  className?: string
  children?: string
}

function CodeBlock({ className, children }: CodeBlockProps) {
  const [copied, setCopied] = useState(false)

  const codeText = typeof children === 'string' ? children.trim() : String(children ?? '')

  // Extract language from className (react-markdown passes "language-xxx")
  const langMatch = className?.match(/language-(\w+)/)
  const lang = langMatch ? langMatch[1] : ''

  let highlighted = codeText
  try {
    if (lang && hljs.getLanguage(lang)) {
      highlighted = hljs.highlight(codeText, { language: lang }).value
    } else {
      highlighted = hljs.highlightAuto(codeText).value
    }
  } catch {
    highlighted = codeText
  }

  const handleCopy = async () => {
    try {
      await navigator.clipboard.writeText(codeText)
      setCopied(true)
      setTimeout(() => setCopied(false), 2000)
    } catch {
      // fallback
      const ta = document.createElement('textarea')
      ta.value = codeText
      document.body.appendChild(ta)
      ta.select()
      document.execCommand('copy')
      document.body.removeChild(ta)
      setCopied(true)
      setTimeout(() => setCopied(false), 2000)
    }
  }

  const lineCount = codeText.split('\n').length

  return (
    <div className="xbot-codeblock">
      <div className="xbot-codeblock-header">
        <span>{lang || 'code'}{lineCount > 1 && <span className="ml-2 text-slate-600 text-[10px]">{lineCount} lines</span>}</span>
        <button onClick={handleCopy} className="xbot-codeblock-copy">
          {copied ? '✓ Copied' : 'Copy'}
        </button>
      </div>
      <pre className="xbot-codeblock-pre">
        <code dangerouslySetInnerHTML={{ __html: highlighted }} />
      </pre>
    </div>
  )
}

// Check if a React node tree contains a checkbox element
function containsCheckbox(children: React.ReactNode): boolean {
  if (!children) return false
  if (Array.isArray(children)) return children.some(containsCheckbox)
  if (typeof children === 'object' && children !== null && 'type' in children) {
    const child = children as { type: string | symbol; props?: Record<string, unknown> }
    if (child.type === 'input') return true
    const childProps = (children as React.ReactElement).props
    if (childProps && typeof childProps === 'object' && 'children' in childProps) {
      return containsCheckbox(childProps.children as React.ReactNode)
    }
  }
  return false
}

// Returns components for react-markdown's components prop
export function getCodeBlockProps() {
  return {
    code(props: { className?: string; children?: React.ReactNode; inline?: boolean }) {
      const lang = props.className?.replace('language-', '')
      const codeStr = String(props.children ?? '')

      // Inline code (no className or in a span)
      if (!lang && !codeStr.includes('\n')) {
        return (
          <code className="xbot-inline-code">
            {props.children}
          </code>
        )
      }

      // Mermaid diagram — render as SVG instead of code block
      if (lang === 'mermaid') {
        // Dynamic import to avoid loading mermaid bundle unless needed
        return <MermaidBlock code={codeStr} />
      }

      return <CodeBlock className={props.className}>{codeStr}</CodeBlock>
    },
    checkbox(props: { checked?: boolean }) {
      return (
        <input
          type="checkbox"
          disabled
          checked={!!props.checked}
          style={{ margin: '0 6px 0 0', accentColor: '#3b82f6', cursor: 'default', pointerEvents: 'none' }}
        />
      )
    },
    li(props: { children?: React.ReactNode; className?: string }) {
      const hasCheckbox = containsCheckbox(props.children)

      if (hasCheckbox) {
        return (
          <li style={{ display: 'flex', alignItems: 'flex-start', gap: 0 }}>
            {props.children}
          </li>
        )
      }

      // react-markdown checkbox plugin uses className "task-list-item checked" for [x]
      if (props.className && /task-list-item/.test(props.className)) {
        return (
          <li
            style={{
              display: 'flex',
              alignItems: 'flex-start',
              listStyle: 'none',
              marginLeft: '-1.5em',
            }}
            className={props.className}
          >
            {props.children}
          </li>
        )
      }

      return <li>{props.children}</li>
    },
    a(props: { href?: string; children?: React.ReactNode }) {
      return <a href={props.href} target="_blank" rel="noopener noreferrer">{props.children}</a>
    },
    pre(props: { children?: React.ReactNode }) {
      // Unwrap react-markdown's outer <pre> — the code component already
      // renders its own container (.xbot-codeblock / .mermaid-wrapper / etc.)
      return <>{props.children}</>
    },
    table(props: { children?: React.ReactNode }) {
      return <div className="table-wrapper"><table>{props.children}</table></div>
    },
  }
}
