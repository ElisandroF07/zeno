// Todo erro da API vem como {"erro": "..."} — inclusive a saída crua do
// `nginx -t`. Centralizado aqui para a mensagem chegar inteira na tela em vez
// de virar "Failed to fetch".
export async function api<T>(url: string, init?: RequestInit): Promise<T> {
  const res = await fetch(url, {
    ...init,
    headers: init?.body ? { 'Content-Type': 'application/json' } : undefined,
  })
  const texto = await res.text()
  const corpo = texto ? JSON.parse(texto) : null
  if (!res.ok) throw new Error(corpo?.erro ?? `HTTP ${res.status}`)
  return corpo as T
}

export const msg = (e: unknown) => (e instanceof Error ? e.message : String(e))
