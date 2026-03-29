import { useRef, useState } from 'react'

export interface PendingFile {
  id: string        // file_id from server
  name: string
  size: number
}

interface FileUploadProps {
  onUpload: (fileId: string, name: string) => void
  onRemove: (fileId: string) => void
  disabled: boolean
}

const MAX_FILE_SIZE = 10 * 1024 * 1024 // 10MB

function showToast(message: string) {
  // Remove existing toasts
  document.querySelectorAll('.file-upload-toast').forEach((el) => el.remove())

  const toast = document.createElement('div')
  toast.className = 'file-upload-toast'
  toast.textContent = message
  document.body.appendChild(toast)

  setTimeout(() => {
    toast.classList.add('file-upload-toast-hide')
    setTimeout(() => toast.remove(), 300)
  }, 3000)
}

export function uploadFile(file: File): Promise<{ ok: boolean; file_id: string; name: string; error?: string }> {
  return new Promise((resolve) => {
    if (file.size > MAX_FILE_SIZE) {
      resolve({ ok: false, file_id: '', name: file.name, error: '文件超过 10MB 限制' })
      return
    }

    const formData = new FormData()
    formData.append('file', file)

    fetch('/api/files/upload', { method: 'POST', body: formData })
      .then((r) => r.json())
      .then((data) => {
        if (data.ok && data.file_id) {
          resolve({ ok: true, file_id: data.file_id, name: data.name })
        } else {
          resolve({ ok: false, file_id: '', name: file.name, error: data.error || '上传失败' })
        }
      })
      .catch((err) => {
        resolve({ ok: false, file_id: '', name: file.name, error: err.message })
      })
  })
}

// Hook to handle files from paste events
export function usePasteUpload(onUpload: (fileId: string, name: string) => void, disabled: boolean) {
  const handlePaste = async (e: React.ClipboardEvent | ClipboardEvent) => {
    if (disabled) return
    const clipboardEvent = e as ClipboardEvent
    const files = clipboardEvent.clipboardData?.files
    if (!files || files.length === 0) return

    // Only handle image pastes — let text pastes through
    const imageFile = Array.from(files).find((f) => f.type.startsWith('image/'))
    if (!imageFile) return

    e.preventDefault()
    const result = await uploadFile(imageFile)
    if (result.ok) {
      onUpload(result.file_id, result.name)
    } else {
      showToast(result.error || '上传失败')
    }
  }
  return handlePaste
}

export default function FileUpload({ onUpload, disabled }: Omit<FileUploadProps, 'onRemove'>) {
  const inputRef = useRef<HTMLInputElement>(null)
  const [uploading, setUploading] = useState(false)

  const handleFiles = async (files: FileList | null) => {
    if (!files || files.length === 0) return
    setUploading(true)

    for (const file of Array.from(files)) {
      const result = await uploadFile(file)
      if (result.ok) {
        onUpload(result.file_id, result.name)
      } else {
        showToast(result.error || '上传失败')
      }
    }

    setUploading(false)
    // Reset input so same file can be re-selected
    if (inputRef.current) inputRef.current.value = ''
  }

  return (
    <button
      className="file-upload-btn"
      onClick={() => inputRef.current?.click()}
      disabled={disabled || uploading}
      title="上传文件"
    >
      {uploading ? '⏳' : '📎'}
      <input
        ref={inputRef}
        type="file"
        multiple
        className="hidden"
        onChange={(e) => handleFiles(e.target.files)}
      />
    </button>
  )
}
