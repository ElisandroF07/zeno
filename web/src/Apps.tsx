import { useCallback, useEffect, useRef, useState } from 'react'
import { toast } from 'sonner'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { api, msg } from '@/lib/api'

type Deploy = {
  id: number
  imagem_tag: string
  status: 'building' | 'success' | 'failed'
  commit_sha: string
  criado_em: string
  atual: boolean
}

type App = {
  id: number
  nome: string
  repo_url: string
  branch: string
  porta: number
  dominio: string
  estado: string
  container: string
  ultimo_deploy?: Deploy
}

export function Apps() {
  const [apps, setApps] = useState<App[]>([])
  const [logDe, setLogDe] = useState<number | null>(null)
  const [historicoDe, setHistoricoDe] = useState<App | null>(null)

  const carregar = useCallback(() => {
    api<App[]>('/api/apps')
      .then(setApps)
      .catch((e) => toast.error('Não foi possível listar as apps', { description: msg(e) }))
  }, [])

  useEffect(() => carregar(), [carregar])

  return (
    <div className="flex flex-col gap-4">
      {apps.map((a) => (
        <CardApp
          key={a.id}
          app={a}
          aoMudar={carregar}
          aoVerLog={setLogDe}
          aoVerHistorico={setHistoricoDe}
        />
      ))}

      <NovaApp aoCriar={carregar} />

      {logDe !== null && (
        <DialogLog
          deployID={logDe}
          aoFechar={() => {
            setLogDe(null)
            carregar()
          }}
        />
      )}

      {historicoDe && (
        <DialogHistorico
          app={historicoDe}
          aoFechar={() => setHistoricoDe(null)}
          aoRollback={(deployID) => {
            setHistoricoDe(null)
            setLogDe(deployID)
          }}
        />
      )}
    </div>
  )
}

function CardApp({
  app,
  aoMudar,
  aoVerLog,
  aoVerHistorico,
}: {
  app: App
  aoMudar: () => void
  aoVerLog: (id: number) => void
  aoVerHistorico: (app: App) => void
}) {
  const [disparando, setDisparando] = useState(false)
  const d = app.ultimo_deploy

  async function deploy() {
    setDisparando(true)
    try {
      const res = await api<{ deploy_id: number }>(`/api/apps/${app.id}/deploy`, { method: 'POST' })
      aoVerLog(res.deploy_id)
      aoMudar()
    } catch (e) {
      toast.error('Não foi possível iniciar o deploy', { description: msg(e) })
    } finally {
      setDisparando(false)
    }
  }

  return (
    <Card>
      <CardHeader>
        <div className="flex flex-wrap items-start justify-between gap-3">
          <div>
            <CardTitle className="flex items-center gap-2 text-base">
              {app.nome}
              <Badge variant={app.estado === 'running' ? 'default' : 'outline'}>{app.estado}</Badge>
              {d && (
                <Badge
                  variant={
                    d.status === 'success'
                      ? 'secondary'
                      : d.status === 'failed'
                        ? 'destructive'
                        : 'outline'
                  }
                >
                  {d.status === 'building' ? 'buildando…' : d.status}
                </Badge>
              )}
            </CardTitle>
            <CardDescription className="font-mono text-xs">
              {app.repo_url} · {app.branch} · porta {app.porta}
              {app.dominio && ` · ${app.dominio}`}
            </CardDescription>
          </div>
          <div className="flex gap-2">
            {d && (
              <Button variant="outline" size="sm" onClick={() => aoVerLog(d.id)}>
                Ver log
              </Button>
            )}
            <Button variant="outline" size="sm" onClick={() => aoVerHistorico(app)}>
              Histórico
            </Button>
            <Button size="sm" onClick={deploy} disabled={disparando}>
              {disparando ? 'Iniciando…' : 'Deploy'}
            </Button>
          </div>
        </div>
      </CardHeader>
      {(app.container || d?.imagem_tag) && (
        <CardContent className="text-muted-foreground flex flex-col gap-1 font-mono text-xs">
          {app.container && <span>container: {app.container}</span>}
          {d?.imagem_tag && <span>imagem: {d.imagem_tag}</span>}
        </CardContent>
      )}
    </Card>
  )
}

function DialogLog({ deployID, aoFechar }: { deployID: number; aoFechar: () => void }) {
  const [linhas, setLinhas] = useState<string[]>([])
  const [fim, setFim] = useState<string | null>(null)
  const fundo = useRef<HTMLDivElement>(null)

  useEffect(() => {
    const es = new EventSource(`/api/deploys/${deployID}/log`)

    es.onmessage = (e) => setLinhas((atual) => [...atual, e.data])
    es.addEventListener('fim', (e) => {
      setFim((e as MessageEvent).data)
      es.close()
    })
    es.addEventListener('erro', (e) => {
      setFim(`erro: ${(e as MessageEvent).data}`)
      es.close()
    })
    // onerror também dispara quando o servidor fecha o stream de propósito;
    // sem fechar aqui o EventSource ficaria reconectando em loop.
    es.onerror = () => es.close()

    return () => es.close()
  }, [deployID])

  useEffect(() => {
    fundo.current?.scrollIntoView({ behavior: 'smooth' })
  }, [linhas])

  return (
    <Dialog open onOpenChange={(aberto) => !aberto && aoFechar()}>
      <DialogContent className="sm:max-w-3xl">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            Deploy #{deployID}
            {fim ? (
              <Badge variant={fim === 'success' ? 'secondary' : 'destructive'}>{fim}</Badge>
            ) : (
              <Badge variant="outline">rodando…</Badge>
            )}
          </DialogTitle>
          <DialogDescription>Log ao vivo do build, via SSE.</DialogDescription>
        </DialogHeader>
        <pre className="bg-muted/50 h-96 overflow-auto rounded-md border p-3 font-mono text-xs">
          {linhas.length === 0 ? 'aguardando saída…' : linhas.join('\n')}
          <div ref={fundo} />
        </pre>
      </DialogContent>
    </Dialog>
  )
}

function DialogHistorico({
  app,
  aoFechar,
  aoRollback,
}: {
  app: App
  aoFechar: () => void
  aoRollback: (deployID: number) => void
}) {
  const [deploys, setDeploys] = useState<Deploy[] | null>(null)
  const [voltando, setVoltando] = useState<number | null>(null)

  useEffect(() => {
    api<Deploy[]>(`/api/apps/${app.id}/deploys`)
      .then(setDeploys)
      .catch((e) => toast.error('Não foi possível carregar o histórico', { description: msg(e) }))
  }, [app.id])

  async function rollback(deployID: number) {
    setVoltando(deployID)
    try {
      const res = await api<{ deploy_id: number }>(`/api/apps/${app.id}/rollback/${deployID}`, {
        method: 'POST',
      })
      toast.success('Rollback iniciado — reaproveitando a imagem, sem rebuild')
      aoRollback(res.deploy_id)
    } catch (e) {
      toast.error('Rollback recusado', { description: msg(e) })
      setVoltando(null)
    }
  }

  return (
    <Dialog open onOpenChange={(aberto) => !aberto && aoFechar()}>
      <DialogContent className="sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>Histórico — {app.nome}</DialogTitle>
          <DialogDescription>
            Rollback reaproveita a imagem já buildada daquele deploy — sem clonar, sem buildar de
            novo.
          </DialogDescription>
        </DialogHeader>

        <div className="flex max-h-96 flex-col gap-2 overflow-y-auto">
          {deploys === null && <p className="text-muted-foreground text-sm">Carregando…</p>}
          {deploys?.length === 0 && (
            <p className="text-muted-foreground text-sm">Nenhum deploy ainda.</p>
          )}
          {deploys?.map((d) => (
            <div
              key={d.id}
              className="flex flex-wrap items-center justify-between gap-2 rounded-md border px-3 py-2"
            >
              <div className="flex items-center gap-2">
                <Badge
                  variant={
                    d.status === 'success' ? 'secondary' : d.status === 'failed' ? 'destructive' : 'outline'
                  }
                >
                  {d.status}
                </Badge>
                {d.atual && <Badge>no ar</Badge>}
                <div className="font-mono text-xs">
                  <div>{d.imagem_tag || '—'}</div>
                  <div className="text-muted-foreground">
                    {d.commit_sha && `${d.commit_sha} · `}
                    {d.criado_em}
                  </div>
                </div>
              </div>
              {d.status === 'success' && !d.atual && (
                <Button
                  size="sm"
                  variant="outline"
                  disabled={voltando !== null}
                  onClick={() => rollback(d.id)}
                >
                  {voltando === d.id ? 'Voltando…' : 'Voltar para esta versão'}
                </Button>
              )}
            </div>
          ))}
        </div>
      </DialogContent>
    </Dialog>
  )
}

type Deteccao = {
  framework: string
  estrategia: string
  porta: number
  comentario: string
}

function NovaApp({ aoCriar }: { aoCriar: () => void }) {
  const [form, setForm] = useState({
    nome: '',
    repo_url: '',
    branch: 'main',
    porta: 3000,
    dominio: '',
  })
  const [salvando, setSalvando] = useState(false)
  const [detectando, setDetectando] = useState(false)
  const [deteccao, setDeteccao] = useState<Deteccao | null>(null)

  async function detectar() {
    setDetectando(true)
    setDeteccao(null)
    try {
      const d = await api<Deteccao>('/api/apps/detectar', {
        method: 'POST',
        body: JSON.stringify({ repo_url: form.repo_url, branch: form.branch }),
      })
      setDeteccao(d)
      // Só troca a porta se o detector achou uma — nunca sobrescreve o que
      // o usuário já digitou com um zero.
      if (d.porta > 0) setForm((f) => ({ ...f, porta: d.porta }))
    } catch (e) {
      toast.error('Não foi possível detectar', { description: msg(e) })
    } finally {
      setDetectando(false)
    }
  }

  async function criar() {
    setSalvando(true)
    try {
      await api('/api/apps', { method: 'POST', body: JSON.stringify(form) })
      toast.success(`app ${form.nome} criada`)
      setForm({ nome: '', repo_url: '', branch: 'main', porta: 3000, dominio: '' })
      aoCriar()
    } catch (e) {
      toast.error('Não foi possível criar', { description: msg(e) })
    } finally {
      setSalvando(false)
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-base">Nova app</CardTitle>
        <CardDescription>
          Dockerfile do projeto ganha; sem ele, o build cai no Nixpacks.
        </CardDescription>
      </CardHeader>
      <CardContent>
        <div className="grid gap-3 sm:grid-cols-2">
          <Campo
            id="nome"
            label="Nome"
            valor={form.nome}
            onChange={(v) => setForm({ ...form, nome: v })}
            placeholder="minha-app"
          />
          <div className="flex flex-col gap-1.5">
            <Label htmlFor="repo_url" className="text-xs">
              Repositório
            </Label>
            <div className="flex gap-2">
              <Input
                id="repo_url"
                value={form.repo_url}
                onChange={(e) => {
                  setForm({ ...form, repo_url: e.target.value })
                  setDeteccao(null) // repo mudou, o palpite anterior não vale mais
                }}
                placeholder="https://github.com/user/repo.git"
                className="font-mono"
              />
              <Button
                type="button"
                variant="outline"
                onClick={detectar}
                disabled={detectando || !form.repo_url}
              >
                {detectando ? 'Clonando…' : 'Detectar'}
              </Button>
            </div>
          </div>
          <Campo
            id="branch"
            label="Branch"
            valor={form.branch}
            onChange={(v) => setForm({ ...form, branch: v })}
          />
          <Campo
            id="porta"
            label="Porta que a app escuta"
            valor={String(form.porta)}
            onChange={(v) => setForm({ ...form, porta: Number(v) || 0 })}
          />
          <Campo
            id="dominio"
            label="Domínio (opcional)"
            valor={form.dominio}
            onChange={(v) => setForm({ ...form, dominio: v })}
            placeholder="app.meudominio.com"
          />
        </div>

        {deteccao && (
          <div className="bg-muted/50 mt-4 rounded-md border p-3 text-sm">
            <p>
              Detectado: <span className="font-medium">{deteccao.framework}</span>
              {deteccao.porta > 0 && (
                <>
                  {' '}
                  · porta sugerida <span className="font-mono">{deteccao.porta}</span>
                </>
              )}
            </p>
            {deteccao.comentario && (
              <p className="text-muted-foreground mt-1 text-xs">{deteccao.comentario}</p>
            )}
          </div>
        )}

        <div className="mt-4">
          <Button onClick={criar} disabled={salvando || !form.nome || !form.repo_url}>
            {salvando ? 'Criando…' : 'Criar app'}
          </Button>
        </div>
      </CardContent>
    </Card>
  )
}

function Campo({
  id,
  label,
  valor,
  onChange,
  placeholder,
}: {
  id: string
  label: string
  valor: string
  onChange: (v: string) => void
  placeholder?: string
}) {
  return (
    <div className="flex flex-col gap-1.5">
      <Label htmlFor={id} className="text-xs">
        {label}
      </Label>
      <Input
        id={id}
        value={valor}
        onChange={(e) => onChange(e.target.value)}
        placeholder={placeholder}
        className="font-mono"
      />
    </div>
  )
}
