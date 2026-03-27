import { useEffect, useState } from 'react'
import LoginPage from './LoginPage'
import ChatPage from './ChatPage'

function App() {
  const [authed, setAuthed] = useState<boolean | null>(null)

  useEffect(() => {
    // Check if already logged in by trying to fetch history
    fetch('/api/history?limit=1')
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
