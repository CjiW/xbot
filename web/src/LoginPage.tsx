import { useState } from 'react'

interface LoginPageProps {
  onLogin: () => void
}

export default function LoginPage({ onLogin }: LoginPageProps) {
  const [isRegister, setIsRegister] = useState(false)
  const [showFeishu, setShowFeishu] = useState(false)
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
      if (showFeishu) {
        const res = await fetch('/api/auth/feishu-login', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ feishu_user_id: feishuUserId, password }),
        })
        const data = await res.json()
        if (!data.ok) {
          setError(data.message || '飞书登录失败')
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
        setError(data.message || '操作失败')
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
          setError('注册成功但登录失败，请手动登录')
          setIsRegister(false)
          return
        }
      }

      onLogin()
    } catch {
      setError('网络错误')
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
          <h2 className="text-lg font-semibold text-white mb-4">
            {showFeishu ? '飞书账号登录' : isRegister ? '创建账号' : '登录'}
          </h2>

          {error && (
            <div className="bg-red-900/30 border border-red-800 text-red-300 text-sm rounded-lg px-3 py-2 mb-4">
              {error}
            </div>
          )}

          {showFeishu ? (
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
                <label className="block text-sm text-slate-300 mb-1">用户名</label>
                <input
                  type="text"
                  value={username}
                  onChange={(e) => setUsername(e.target.value)}
                  className="w-full bg-slate-700 border border-slate-600 rounded-lg px-3 py-2 text-white placeholder-slate-400 focus:outline-none focus:border-blue-500 focus:ring-1 focus:ring-blue-500"
                  placeholder="输入用户名"
                  required
                  maxLength={64}
                />
              </div>

              <div className="mb-6">
                <label className="block text-sm text-slate-300 mb-1">密码</label>
                <input
                  type="password"
                  value={password}
                  onChange={(e) => setPassword(e.target.value)}
                  className="w-full bg-slate-700 border border-slate-600 rounded-lg px-3 py-2 text-white placeholder-slate-400 focus:outline-none focus:border-blue-500 focus:ring-1 focus:ring-blue-500"
                  placeholder="输入密码"
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
            {loading ? '...' : showFeishu ? '飞书登录' : isRegister ? '注册' : '登录'}
          </button>

          {!showFeishu && (
            <div className="mt-4 text-center">
              <button
                type="button"
                onClick={() => { setIsRegister(!isRegister); setError('') }}
                className="text-sm text-blue-400 hover:text-blue-300"
              >
                {isRegister ? '已有账号？登录' : '没有账号？注册'}
              </button>
            </div>
          )}

          {/* 飞书登录入口：降级为底部链接 */}
          <div className="mt-4 text-center">
            {showFeishu ? (
              <button
                type="button"
                onClick={() => { setShowFeishu(false); setError('') }}
                className="text-xs text-slate-500 hover:text-slate-400"
              >
                ← 返回账号密码登录
              </button>
            ) : (
              <button
                type="button"
                onClick={() => { setShowFeishu(true); setError('') }}
                className="text-xs text-slate-500 hover:text-slate-400"
              >
                通过飞书用户 ID 登录 →
              </button>
            )}
          </div>
        </form>
      </div>
    </div>
  )
}
