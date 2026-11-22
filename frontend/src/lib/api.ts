// All requests are relative to <base href>, so the panel works under any path.
let csrf = ''
let onUnauthorized: () => void = () => {}

export function setCsrf(token: string) {
  csrf = token
}

export function setUnauthorizedHandler(fn: () => void) {
  onUnauthorized = fn
}

export class ApiError extends Error {
  status: number
  constructor(status: number, message: string) {
    super(message)
    this.status = status
  }
}

async function parseError(res: Response): Promise<ApiError> {
  let msg = `Сервер ответил ${res.status}`
  try {
    const body = await res.json()
    if (body?.error) msg = body.error
  } catch {
    /* not JSON */
  }
  return new ApiError(res.status, msg)
}

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const headers: Record<string, string> = { 'X-Requested-With': 'MRSmith' }
  if (method !== 'GET') headers['X-CSRF-Token'] = csrf
  let payload: BodyInit | undefined
  if (body instanceof FormData) {
    payload = body
  } else if (body !== undefined) {
    headers['Content-Type'] = 'application/json'
    payload = JSON.stringify(body)
  }
  let res: Response
  try {
    res = await fetch('api/' + path, { method, headers, body: payload, credentials: 'same-origin', cache: 'no-store' })
  } catch {
    throw new ApiError(0, 'Нет связи с сервером')
  }
  if (res.status === 401 && path !== 'auth/login' && path !== 'auth/me') onUnauthorized()
  if (!res.ok) throw await parseError(res)
  if (res.status === 204) return undefined as T
  if ((res.headers.get('Content-Type') || '').includes('application/json')) return (await res.json()) as T
  return (await res.blob()) as T
}

export const api = {
  get: <T>(path: string) => request<T>('GET', path),
  post: <T>(path: string, body?: unknown) => request<T>('POST', path, body),
  put: <T>(path: string, body?: unknown) => request<T>('PUT', path, body),
  patch: <T>(path: string, body?: unknown) => request<T>('PATCH', path, body),
  del: <T>(path: string) => request<T>('DELETE', path),
}

/** Sends a POST that returns a file and keeps the server-provided file name. */
export async function downloadPost(path: string, body: FormData): Promise<{ blob: Blob; name: string }> {
  const res = await fetch('api/' + path, {
    method: 'POST',
    headers: { 'X-Requested-With': 'MRSmith', 'X-CSRF-Token': csrf },
    body,
    credentials: 'same-origin',
  })
  if (!res.ok) throw await parseError(res)
  return { blob: await res.blob(), name: fileNameFrom(res.headers.get('Content-Disposition')) || 'rules' }
}

function fileNameFrom(disposition: string | null): string {
  if (!disposition) return ''
  const star = /filename\*=utf-8''([^;]+)/i.exec(disposition)
  if (star) return decodeURIComponent(star[1])
  const plain = /filename="?([^";]+)"?/i.exec(disposition)
  return plain ? plain[1] : ''
}

export function saveBlob(blob: Blob, name: string) {
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = name
  document.body.appendChild(a)
  a.click()
  a.remove()
  setTimeout(() => URL.revokeObjectURL(url), 10_000)
}

/** Upload with progress reporting (fetch cannot report upload progress). */
export function upload<T>(path: string, form: FormData, onProgress?: (fraction: number) => void) {
  const xhr = new XMLHttpRequest()
  const promise = new Promise<T>((resolve, reject) => {
    xhr.open('POST', 'api/' + path)
    xhr.setRequestHeader('X-Requested-With', 'MRSmith')
    xhr.setRequestHeader('X-CSRF-Token', csrf)
    xhr.responseType = 'text'
    xhr.upload.onprogress = (e) => {
      if (e.lengthComputable && onProgress) onProgress(e.loaded / e.total)
    }
    xhr.onload = () => {
      let body: { error?: string } | undefined
      try {
        body = JSON.parse(xhr.responseText)
      } catch {
        body = undefined
      }
      if (xhr.status >= 200 && xhr.status < 300) resolve(body as T)
      else {
        if (xhr.status === 401) onUnauthorized()
        reject(new ApiError(xhr.status, body?.error || `Сервер ответил ${xhr.status}`))
      }
    }
    xhr.onerror = () => reject(new ApiError(0, 'Нет связи с сервером'))
    xhr.onabort = () => reject(new ApiError(0, 'Загрузка отменена'))
    xhr.send(form)
  })
  return { promise, abort: () => xhr.abort() }
}

export function errorText(e: unknown): string {
  if (e instanceof Error) return e.message
  return String(e)
}
