import { useEffect, useState } from 'react'
import LoginPage from './LoginPage'
import ChatPage from './ChatPage'

// Apply saved theme immediately before React renders (prevents flash)
const savedTheme = localStorage.getItem('xbot-theme') || 'dark'
document.documentElement.setAttribute('data-theme', savedTheme)

function App() {
  const [authed, setAuthed] = useState<boolean | null>(null)

  useEffect(() => {
    // Check if already logged in by trying to fetch history
    fetch('/api/history')
      .then((r) => {
        setAuthed(r.ok)
      })
      .catch(() => setAuthed(false))
  }, [])

  if (authed === null) {
    return (
      <div className="flex items-center justify-center min-h-screen bg-slate-900 text-slate-400">
        Loading...
      </div>
    )
  }

  return authed ? (
    <ChatPage onLogout={() => setAuthed(false)} />
  ) : (
    <LoginPage onLogin={() => setAuthed(true)} />
  )
}

export default App
