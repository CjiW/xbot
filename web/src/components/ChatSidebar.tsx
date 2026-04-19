import React, { useState, useEffect, useCallback } from 'react'

interface ChatInfo {
  chat_id: string
  label: string
  last_active: string
  preview: string
  is_current: boolean
}

interface ChatSidebarProps {
  onSwitchChat: (chatID: string) => void
  onNewChat: () => void
  currentChatID: string
}

export default function ChatSidebar({ onSwitchChat, onNewChat, currentChatID }: ChatSidebarProps) {
  const [chats, setChats] = useState<ChatInfo[]>([])
  const [loading, setLoading] = useState(false)
  const [collapsed, setCollapsed] = useState(false)

  const fetchChats = useCallback(async () => {
    setLoading(true)
    try {
      const resp = await fetch('/api/chats')
      const data = await resp.json()
      if (data.ok) setChats(data.chats || [])
    } catch {}
    setLoading(false)
  }, [])

  useEffect(() => { fetchChats() }, [fetchChats])

  const handleSwitch = async (chatID: string) => {
    if (chatID === currentChatID) return
    try {
      await fetch(`/api/chats/${encodeURIComponent(chatID)}/switch`, { method: 'POST' })
      onSwitchChat(chatID)
    } catch {}
  }

  const handleCreate = async () => {
    try {
      const resp = await fetch('/api/chats', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ label: '' }),
      })
      const data = await resp.json()
      if (data.ok && data.chat_id) {
        await fetch(`/api/chats/${encodeURIComponent(data.chat_id)}/switch`, { method: 'POST' })
        onNewChat()
        fetchChats()
      }
    } catch {}
  }

  const handleDelete = async (e: React.MouseEvent, chatID: string) => {
    e.stopPropagation()
    if (!confirm('确定要删除此会话吗？')) return
    try {
      await fetch(`/api/chats/${encodeURIComponent(chatID)}`, { method: 'DELETE' })
      if (chatID === currentChatID) {
        // Switch back to default
        const defaultID = chats.find(c => c.label === '默认会话')?.chat_id
        if (defaultID) onSwitchChat(defaultID)
      }
      fetchChats()
    } catch {}
  }

  if (collapsed) {
    return (
      <div className="flex flex-col items-center py-2 px-1 bg-slate-900/80 border-r border-slate-700/50"
           onClick={() => setCollapsed(false)}
           title="展开会话列表"
           style={{ cursor: 'pointer' }}>
        <span className="text-lg">💬</span>
        <span className="text-[10px] text-slate-500 mt-1">{chats.length}</span>
      </div>
    )
  }

  return (
    <div className="flex flex-col w-56 bg-slate-900/80 border-r border-slate-700/50 shrink-0">
      {/* Header */}
      <div className="flex items-center justify-between px-3 py-2 border-b border-slate-700/50">
        <span className="text-sm font-medium text-slate-300">💬 会话</span>
        <div className="flex items-center gap-1">
          <button
            onClick={handleCreate}
            className="text-xs px-1.5 py-0.5 rounded hover:bg-slate-700 text-slate-400 hover:text-slate-200"
            title="新建会话"
          >+</button>
          <button
            onClick={() => setCollapsed(true)}
            className="text-xs px-1.5 py-0.5 rounded hover:bg-slate-700 text-slate-400 hover:text-slate-200"
            title="收起"
          >◀</button>
        </div>
      </div>

      {/* Chat List */}
      <div className="flex-1 overflow-y-auto py-1">
        {loading ? (
          <div className="text-center py-4 text-slate-500 text-xs">加载中...</div>
        ) : chats.length === 0 ? (
          <div className="text-center py-4 text-slate-500 text-xs">暂无会话</div>
        ) : (
          chats.map((chat) => (
            <div
              key={chat.chat_id}
              className={`group mx-1 px-2 py-1.5 rounded cursor-pointer transition-colors ${
                chat.is_current
                  ? 'bg-indigo-900/30 border-l-2 border-indigo-500'
                  : 'hover:bg-slate-800/50 border-l-2 border-transparent'
              }`}
              onClick={() => handleSwitch(chat.chat_id)}
            >
              <div className="flex items-center gap-1">
                <span className="text-xs truncate flex-1 text-slate-300">{chat.label}</span>
                {chat.is_current && (
                  <span className="text-[10px] text-indigo-400 shrink-0">当前</span>
                )}
                {!chat.is_current && chat.chat_id !== currentChatID && (
                  <button
                    onClick={(e) => handleDelete(e, chat.chat_id)}
                    className="hidden group-hover:block text-[10px] text-slate-600 hover:text-red-400 shrink-0"
                    title="删除"
                  >✕</button>
                )}
              </div>
              {chat.preview && (
                <div className="text-[10px] text-slate-500 mt-0.5 truncate">{chat.preview}</div>
              )}
            </div>
          ))
        )}
      </div>

      {/* Refresh */}
      <div className="border-t border-slate-700/50 px-2 py-1">
        <button
          onClick={fetchChats}
          disabled={loading}
          className="text-[10px] text-slate-500 hover:text-slate-300 w-full text-center"
        >
          {loading ? '...' : '🔄 刷新'}
        </button>
      </div>
    </div>
  )
}
