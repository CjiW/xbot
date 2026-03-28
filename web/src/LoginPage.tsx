import { useState } from 'react'

type LoginMode = 'password' | 'feishu'

interface LoginPageProps {
  onLogin: () => void
}

export default function LoginPage({ onLogin }: LoginPageProps) {
  const [mode, setMode] = useState<LoginMode>('password')
  const [isRegister, setIsRegister] = useState(false)
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [feishuUserId, setFeishuUserId] = useState('')
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(false)

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    setError('')
    setLoading(true)

    try {
      if (mode === 'feishu') {
        const res = await fetch('/api/auth/feishu-login', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ feishu_user_id: feishuUserId, password }),
        })
        const data = await res.json()
        if (!data.ok) {
          setError(data.message || 'Feishu login failed')
          return
        }
        onLogin()
        return
      }

      const url = isRegister ? '/api/auth/register' : '/api/auth/login'
      const res = await fetch(url, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ username, password }),
      })

      const data = await res.json()

      if (!data.ok) {
        setError(data.message || 'Operation failed')
        return
      }

      if (isRegister) {
        // After register, auto-login
        const loginRes = await fetch('/api/auth/login', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ username, password }),
        })
        const loginData = await loginRes.json()
        if (!loginData.ok) {
          setError('Registered but login failed, please login manually')
          setIsRegister(false)
          return
        }
      }

      onLogin()
    } catch {
      setError('Network error')
    } finally {
      setLoading(false)
    }
  }

  return (
    <div className="flex items-center justify-center min-h-screen bg-slate-900 px-4">
      <div className="w-full max-w-sm">
        <div className="text-center mb-8">
          <h1 className="text-3xl font-bold text-white mb-2">🤖 xbot</h1>
          <p className="text-slate-400 text-sm">Web Chat Interface</p>
        </div>

        <form
          onSubmit={handleSubmit}
          className="bg-slate-800 rounded-xl p-6 shadow-lg border border-slate-700"
        >
          {/* Mode tabs */}
          <div className="flex mb-5 bg-slate-900/50 rounded-lg p-1">
            <button
              type="button"
              onClick={() => { setMode('password'); setError('') }}
              className={`flex-1 text-sm py-1.5 rounded-md transition-colors ${
                mode === 'password' ? 'bg-slate-700 text-white' : 'text-slate-400 hover:text-slate-300'
              }`}
            >
              密码登录
            </button>
            <button
              type="button"
              onClick={() => { setMode('feishu'); setError('') }}
              className={`flex-1 text-sm py-1.5 rounded-md transition-colors flex items-center justify-center gap-1.5 ${
                mode === 'feishu' ? 'bg-slate-700 text-white' : 'text-slate-400 hover:text-slate-300'
              }`}
            >
              飞书登录
            </button>
          </div>

          <h2 className="text-lg font-semibold text-white mb-4">
            {mode === 'feishu' ? '飞书账号登录' : isRegister ? 'Create Account' : 'Sign In'}
          </h2>

          {error && (
            <div className="bg-red-900/30 border border-red-800 text-red-300 text-sm rounded-lg px-3 py-2 mb-4">
              {error}
            </div>
          )}

          {mode === 'feishu' ? (
            <>
              <div className="mb-4">
                <label className="block text-sm text-slate-300 mb-1">飞书用户 ID</label>
                <input
                  type="text"
                  value={feishuUserId}
                  onChange={(e) => setFeishuUserId(e.target.value)}
                  className="w-full bg-slate-700 border border-slate-600 rounded-lg px-3 py-2 text-white placeholder-slate-400 focus:outline-none focus:border-blue-500 focus:ring-1 focus:ring-blue-500"
                  placeholder="ou_xxx 或 open_id"
                  required
                  maxLength={128}
                />
              </div>

              <div className="mb-6">
                <label className="block text-sm text-slate-300 mb-1">Web 密码</label>
                <input
                  type="password"
                  value={password}
                  onChange={(e) => setPassword(e.target.value)}
                  className="w-full bg-slate-700 border border-slate-600 rounded-lg px-3 py-2 text-white placeholder-slate-400 focus:outline-none focus:border-blue-500 focus:ring-1 focus:ring-blue-500"
                  placeholder="关联的 Web 账号密码"
                  required
                  maxLength={128}
                />
              </div>

              <p className="text-xs text-slate-500 mb-4">
                需要先在飞书中绑定 Web 账号，使用绑定时设置的密码登录。
              </p>
            </>
          ) : (
            <>
              <div className="mb-4">
                <label className="block text-sm text-slate-300 mb-1">Username</label>
                <input
                  type="text"
                  value={username}
                  onChange={(e) => setUsername(e.target.value)}
                  className="w-full bg-slate-700 border border-slate-600 rounded-lg px-3 py-2 text-white placeholder-slate-400 focus:outline-none focus:border-blue-500 focus:ring-1 focus:ring-blue-500"
                  placeholder="Enter username"
                  required
                  maxLength={64}
                />
              </div>

              <div className="mb-6">
                <label className="block text-sm text-slate-300 mb-1">Password</label>
                <input
                  type="password"
                  value={password}
                  onChange={(e) => setPassword(e.target.value)}
                  className="w-full bg-slate-700 border border-slate-600 rounded-lg px-3 py-2 text-white placeholder-slate-400 focus:outline-none focus:border-blue-500 focus:ring-1 focus:ring-blue-500"
                  placeholder="Enter password"
                  required
                  maxLength={128}
                />
              </div>
            </>
          )}

          <button
            type="submit"
            disabled={loading}
            className="w-full bg-blue-600 hover:bg-blue-700 disabled:bg-blue-800 text-white font-medium py-2 rounded-lg transition-colors"
          >
            {loading ? '...' : mode === 'feishu' ? '飞书登录' : isRegister ? 'Register' : 'Sign In'}
          </button>

          {mode === 'password' && (
            <div className="mt-4 text-center">
              <button
                type="button"
                onClick={() => { setIsRegister(!isRegister); setError('') }}
                className="text-sm text-blue-400 hover:text-blue-300"
              >
                {isRegister ? 'Already have an account? Sign in' : "Don't have an account? Register"}
              </button>
            </div>
          )}
        </form>
      </div>
    </div>
  )
}
