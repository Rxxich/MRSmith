import { type ReactNode, createContext, useCallback, useContext, useEffect, useRef, useState } from 'react'
import { IconAlertTriangle, IconCircleCheck, IconX } from '@tabler/icons-react'
import { Button } from './core'

// ---- Modal -----------------------------------------------------------------

interface ModalProps {
  open: boolean
  onClose: () => void
  title: ReactNode
  children: ReactNode
  footer?: ReactNode
  wide?: boolean
}

/** Native <dialog>: focus trapping, Esc and inert background come for free. */
export function Modal({ open, onClose, title, children, footer, wide }: ModalProps) {
  const ref = useRef<HTMLDialogElement>(null)
  useEffect(() => {
    const d = ref.current
    if (!d) return
    if (open && !d.open) d.showModal()
    if (!open && d.open) d.close()
  }, [open])
  return (
    <dialog
      ref={ref}
      className={wide ? 'modal wide' : 'modal'}
      onCancel={(e) => {
        e.preventDefault()
        onClose()
      }}
      onClick={(e) => {
        if (e.target === ref.current) onClose()
      }}
    >
      {open && (
        <>
          <div className="modal-head">
            <h2>{title}</h2>
            <Button variant="ghost" size="sm" iconOnly onClick={onClose} aria-label="Закрыть" icon={<IconX size={16} />} />
          </div>
          <div className="modal-body">{children}</div>
          {footer && <div className="modal-foot">{footer}</div>}
        </>
      )}
    </dialog>
  )
}

// ---- Toasts ----------------------------------------------------------------

interface Toast {
  id: number
  text: ReactNode
  tone: 'ok' | 'error'
}

interface Overlay {
  toast: (text: ReactNode, tone?: 'ok' | 'error') => void
  confirm: (opts: ConfirmOptions) => Promise<boolean>
}

interface ConfirmOptions {
  title: string
  body?: ReactNode
  confirm: string
  danger?: boolean
}

const OverlayContext = createContext<Overlay | null>(null)

export function OverlayProvider({ children }: { children: ReactNode }) {
  const [toasts, setToasts] = useState<Toast[]>([])
  const [ask, setAsk] = useState<(ConfirmOptions & { resolve: (v: boolean) => void }) | null>(null)
  const seq = useRef(0)

  const toast = useCallback((text: ReactNode, tone: 'ok' | 'error' = 'ok') => {
    const id = ++seq.current
    setToasts((t) => [...t.slice(-3), { id, text, tone }])
    window.setTimeout(() => setToasts((t) => t.filter((x) => x.id !== id)), tone === 'error' ? 7000 : 3500)
  }, [])

  const confirm = useCallback(
    (opts: ConfirmOptions) => new Promise<boolean>((resolve) => setAsk({ ...opts, resolve })),
    [],
  )

  const answer = (v: boolean) => {
    ask?.resolve(v)
    setAsk(null)
  }

  return (
    <OverlayContext.Provider value={{ toast, confirm }}>
      {children}
      <div className="toasts" aria-live="polite">
        {toasts.map((t) => (
          <div key={t.id} className={t.tone === 'error' ? 'toast toast-error' : 'toast'} role={t.tone === 'error' ? 'alert' : 'status'}>
            {t.tone === 'error' ? (
              <IconAlertTriangle size={17} color="var(--rust)" aria-hidden />
            ) : (
              <IconCircleCheck size={17} color="var(--patina)" aria-hidden />
            )}
            <div className="grow">{t.text}</div>
            <button
              type="button"
              className="btn btn-ghost btn-sm btn-icon"
              aria-label="Скрыть"
              onClick={() => setToasts((all) => all.filter((x) => x.id !== t.id))}
            >
              <IconX size={14} />
            </button>
          </div>
        ))}
      </div>
      <Modal
        open={!!ask}
        onClose={() => answer(false)}
        title={ask?.title}
        footer={
          <>
            <Button variant="ghost" onClick={() => answer(false)}>
              Отмена
            </Button>
            <Button variant={ask?.danger ? 'danger' : 'primary'} onClick={() => answer(true)} autoFocus>
              {ask?.confirm}
            </Button>
          </>
        }
      >
        {ask?.body && <div className="dim">{ask.body}</div>}
      </Modal>
    </OverlayContext.Provider>
  )
}

export function useOverlay(): Overlay {
  const ctx = useContext(OverlayContext)
  if (!ctx) throw new Error('OverlayProvider missing')
  return ctx
}
