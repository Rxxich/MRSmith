import { type ReactNode, useEffect, useRef, useState } from 'react'
import { IconUpload } from '@tabler/icons-react'

/** Collects files from a drop event, walking dropped folders. Each file gets
 *  its relative path (folder/name) so a dropped tree keeps its structure. */
export async function filesFromDrop(dt: DataTransfer): Promise<{ file: File; path: string }[]> {
  const items = Array.from(dt.items || [])
  const entries = items
    .map((i) => (typeof i.webkitGetAsEntry === 'function' ? i.webkitGetAsEntry() : null))
    .filter((e): e is FileSystemEntry => !!e)
  if (entries.length === 0) {
    return Array.from(dt.files).map((f) => ({ file: f, path: f.name }))
  }
  const out: { file: File; path: string }[] = []
  const walk = async (entry: FileSystemEntry, prefix: string): Promise<void> => {
    if (entry.isFile) {
      const file = await new Promise<File>((res, rej) => (entry as FileSystemFileEntry).file(res, rej))
      out.push({ file, path: prefix + file.name })
    } else if (entry.isDirectory) {
      const reader = (entry as FileSystemDirectoryEntry).createReader()
      let batch: FileSystemEntry[]
      do {
        batch = await new Promise<FileSystemEntry[]>((res, rej) => reader.readEntries(res, rej))
        for (const child of batch) await walk(child, prefix + entry.name + '/')
      } while (batch.length > 0)
    }
  }
  for (const e of entries) await walk(e, '')
  return out
}

interface DropzoneProps {
  onFiles: (files: { file: File; path: string }[]) => void
  accept?: string
  multiple?: boolean
  title: ReactNode
  hint?: ReactNode
}

export function Dropzone({ onFiles, accept, multiple = true, title, hint }: DropzoneProps) {
  const [over, setOver] = useState(false)
  const input = useRef<HTMLInputElement>(null)
  return (
    <div
      className={over ? 'drop is-over' : 'drop'}
      role="button"
      tabIndex={0}
      onClick={() => input.current?.click()}
      onKeyDown={(e) => {
        if (e.key === 'Enter' || e.key === ' ') {
          e.preventDefault()
          input.current?.click()
        }
      }}
      onDragOver={(e) => {
        e.preventDefault()
        e.stopPropagation()
        setOver(true)
      }}
      onDragLeave={() => setOver(false)}
      onDrop={async (e) => {
        e.preventDefault()
        e.stopPropagation()
        setOver(false)
        const files = await filesFromDrop(e.dataTransfer)
        if (files.length) onFiles(multiple ? files : files.slice(0, 1))
      }}
    >
      <IconUpload size={22} aria-hidden />
      <b>{title}</b>
      {hint && <span className="small">{hint}</span>}
      <input
        ref={input}
        type="file"
        hidden
        accept={accept}
        multiple={multiple}
        onChange={(e) => {
          const list = Array.from(e.target.files || []).map((f) => ({ file: f, path: f.name }))
          if (list.length) onFiles(list)
          e.target.value = ''
        }}
      />
    </div>
  )
}

/** Shows a full-window overlay while files are dragged over the page and
 *  hands them over on drop. */
export function useWindowDrop(onFiles: (files: { file: File; path: string }[]) => void, enabled = true) {
  const [active, setActive] = useState(false)
  const handler = useRef(onFiles)
  handler.current = onFiles
  useEffect(() => {
    if (!enabled) return
    let depth = 0
    const hasFiles = (e: DragEvent) => Array.from(e.dataTransfer?.types || []).includes('Files')
    const enter = (e: DragEvent) => {
      if (!hasFiles(e)) return
      depth++
      setActive(true)
    }
    const leave = (e: DragEvent) => {
      if (!hasFiles(e)) return
      depth = Math.max(0, depth - 1)
      if (depth === 0) setActive(false)
    }
    const over = (e: DragEvent) => {
      if (hasFiles(e)) e.preventDefault()
    }
    const drop = async (e: DragEvent) => {
      if (!hasFiles(e) || !e.dataTransfer) return
      e.preventDefault()
      depth = 0
      setActive(false)
      const files = await filesFromDrop(e.dataTransfer)
      if (files.length) handler.current(files)
    }
    window.addEventListener('dragenter', enter)
    window.addEventListener('dragleave', leave)
    window.addEventListener('dragover', over)
    window.addEventListener('drop', drop)
    return () => {
      window.removeEventListener('dragenter', enter)
      window.removeEventListener('dragleave', leave)
      window.removeEventListener('dragover', over)
      window.removeEventListener('drop', drop)
    }
  }, [enabled])
  return active
}
