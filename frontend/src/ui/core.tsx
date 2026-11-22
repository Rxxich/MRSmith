import {
  type ButtonHTMLAttributes,
  type InputHTMLAttributes,
  type ReactNode,
  type SelectHTMLAttributes,
  type TextareaHTMLAttributes,
  forwardRef,
  useId,
  useState,
} from 'react'
import { IconAlertTriangle, IconCheck, IconCopy, IconInfoCircle, IconLoader2 } from '@tabler/icons-react'
import type { Behavior, Status } from '../lib/types'

type Variant = 'primary' | 'secondary' | 'ghost' | 'danger'

interface ButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: Variant
  size?: 'md' | 'sm'
  icon?: ReactNode
  busy?: boolean
  iconOnly?: boolean
}

export const Button = forwardRef<HTMLButtonElement, ButtonProps>(function Button(
  { variant = 'secondary', size = 'md', icon, busy, iconOnly, className = '', children, disabled, type = 'button', ...rest },
  ref,
) {
  const cls = ['btn', `btn-${variant}`, size === 'sm' && 'btn-sm', iconOnly && 'btn-icon', className].filter(Boolean).join(' ')
  return (
    <button ref={ref} type={type} className={cls} disabled={disabled || busy} aria-busy={busy || undefined} {...rest}>
      {busy ? <IconLoader2 size={size === 'sm' ? 14 : 16} className="spin" aria-hidden /> : icon}
      {children}
    </button>
  )
})

interface FieldProps {
  label: ReactNode
  hint?: ReactNode
  error?: string
  children: (id: string) => ReactNode
  className?: string
}

export function Field({ label, hint, error, children, className }: FieldProps) {
  const id = useId()
  return (
    <div className={['field', className].filter(Boolean).join(' ')}>
      <label className="field-label" htmlFor={id}>
        {label}
      </label>
      {children(id)}
      {error ? <span className="field-error">{error}</span> : hint ? <span className="field-hint">{hint}</span> : null}
    </div>
  )
}

export const Input = forwardRef<HTMLInputElement, InputHTMLAttributes<HTMLInputElement> & { mono?: boolean }>(
  function Input({ mono, className = '', ...rest }, ref) {
    return <input ref={ref} className={['input', mono && 'mono', className].filter(Boolean).join(' ')} {...rest} />
  },
)

export function Textarea(props: TextareaHTMLAttributes<HTMLTextAreaElement>) {
  return <textarea className="textarea" spellCheck={false} {...props} />
}

export function Select({ children, className = 'select', ...rest }: SelectHTMLAttributes<HTMLSelectElement>) {
  return (
    <select className={className} {...rest}>
      {children}
    </select>
  )
}

interface SwitchProps {
  checked: boolean
  onChange: (v: boolean) => void
  label: ReactNode
  hint?: ReactNode
  disabled?: boolean
}

export function Switch({ checked, onChange, label, hint, disabled }: SwitchProps) {
  return (
    <label className="switch">
      <input type="checkbox" role="switch" checked={checked} disabled={disabled} onChange={(e) => onChange(e.target.checked)} />
      <span className="switch-text">
        <span>{label}</span>
        {hint && <small>{hint}</small>}
      </span>
    </label>
  )
}

interface SegmentedProps<T extends string> {
  value: T
  options: { value: T; label: ReactNode; icon?: ReactNode }[]
  onChange: (v: T) => void
  label: string
}

export function Segmented<T extends string>({ value, options, onChange, label }: SegmentedProps<T>) {
  return (
    <div className="segmented" role="group" aria-label={label}>
      {options.map((o) => (
        <button key={o.value} type="button" aria-pressed={o.value === value} onClick={() => onChange(o.value)}>
          {o.icon}
          {o.label}
        </button>
      ))}
    </div>
  )
}

const BEHAVIOR_TEXT: Record<Behavior, string> = { auto: 'авто', domain: 'domain', ipcidr: 'ipcidr', classical: 'classical' }

export function BehaviorTag({ behavior }: { behavior?: Behavior }) {
  const b = behavior || 'auto'
  return <span className={`tag tag-${b}`}>{BEHAVIOR_TEXT[b]}</span>
}

export function StatusDot({ status }: { status: Status | 'running' }) {
  const cls = status === 'running' ? 'dot-pending' : status === 'ok' ? 'dot-ok' : status === 'error' ? 'dot-error' : 'dot-pending'
  return <span className={`dot ${cls}`} aria-hidden />
}

export function Notice({ tone = 'info', children }: { tone?: 'info' | 'warn' | 'error'; children: ReactNode }) {
  const Icon = tone === 'info' ? IconInfoCircle : IconAlertTriangle
  return (
    <div className={`notice notice-${tone}`} role={tone === 'error' ? 'alert' : undefined}>
      <Icon size={17} aria-hidden />
      <div className="grow">{children}</div>
    </div>
  )
}

export function Loading({ children = 'Загрузка…' }: { children?: ReactNode }) {
  return (
    <div className="loading" role="status">
      <IconLoader2 size={18} aria-hidden />
      {children}
    </div>
  )
}

export function Empty({ icon, title, children, action }: { icon: ReactNode; title: string; children?: ReactNode; action?: ReactNode }) {
  return (
    <div className="empty">
      {icon}
      <h3>{title}</h3>
      {children && <p>{children}</p>}
      {action}
    </div>
  )
}

async function copyText(text: string) {
  try {
    await navigator.clipboard.writeText(text)
  } catch {
    const ta = document.createElement('textarea')
    ta.value = text
    document.body.appendChild(ta)
    ta.select()
    document.execCommand('copy')
    ta.remove()
  }
}

/** A small labelled button that copies text, e.g. one link of a rule set. */
export function CopyChip({ text, label, title }: { text: string; label: string; title: string }) {
  const [done, setDone] = useState(false)
  return (
    <button
      type="button"
      className={done ? 'copy-chip done' : 'copy-chip'}
      title={done ? 'Ссылка скопирована' : title}
      aria-label={done ? 'Ссылка скопирована' : title}
      onClick={async () => {
        await copyText(text)
        setDone(true)
        window.setTimeout(() => setDone(false), 1400)
      }}
    >
      {label}
      {done ? <IconCheck size={12} aria-hidden /> : <IconCopy size={12} aria-hidden />}
    </button>
  )
}

/** Copies text and briefly confirms with a check mark. */
export function CopyButton({ text, label = 'Скопировать', size = 'sm' }: { text: string; label?: string; size?: 'sm' | 'md' }) {
  const [done, setDone] = useState(false)
  const copy = async () => {
    await copyText(text)
    setDone(true)
    window.setTimeout(() => setDone(false), 1400)
  }
  return (
    <Button
      variant="ghost"
      size={size}
      iconOnly
      onClick={copy}
      aria-label={done ? 'Скопировано' : label}
      title={done ? 'Скопировано' : label}
      icon={done ? <IconCheck size={15} color="var(--patina)" /> : <IconCopy size={15} />}
    />
  )
}

export function CopyField({ value }: { value: string }) {
  return (
    <div className="copy">
      <code title={value}>{value}</code>
      <CopyButton text={value} label="Скопировать ссылку" />
    </div>
  )
}

/** Renders a rule with its syntax prefix dimmed so the host stands out. */
export function RuleText({ rule }: { rule: string }) {
  const comma = rule.indexOf(',')
  if (comma > 0 && /^[A-Z0-9-]+$/.test(rule.slice(0, comma))) {
    return (
      <>
        <span className="rule-type">{rule.slice(0, comma + 1)}</span>
        {rule.slice(comma + 1)}
      </>
    )
  }
  const m = /^(\+\.|\.|\*\.)/.exec(rule)
  if (m) {
    return (
      <>
        <span className="rule-prefix">{m[1]}</span>
        {rule.slice(m[1].length)}
      </>
    )
  }
  return <>{rule}</>
}
