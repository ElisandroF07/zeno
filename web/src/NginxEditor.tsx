import { useCallback, useEffect, useState } from 'react'
import { toast } from 'sonner'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Separator } from '@/components/ui/separator'
import { Switch } from '@/components/ui/switch'
import { api, msg } from '@/lib/api'

type Location = {
  id: number
  path: string
  tipo: string
  alvo: string
  dinamico: boolean
  stream: boolean
}

type Server = {
  id: number
  server_name: string
  ssl: boolean
  gerado: boolean
  padrao: boolean
  cert_pronto: boolean
  locations: Location[] | null
}

type Servers = {
  gerados: Server[]
  do_usuario: Server[]
  conf_gerada: string
  conf_usuario: string
}

type Status = {
  fase: 'provisionando' | 'pronto' | 'erro'
  rodando: boolean
  existe: boolean
  imagem: string
  aplicado_em: string
  ok: boolean
  erro: string
}

export function NginxEditor() {
  const [status, setStatus] = useState<Status | null>(null)
  const [dados, setDados] = useState<Servers | null>(null)
  const [erro, setErro] = useState<string | null>(null)

  const carregar = useCallback(async () => {
    try {
      const [s, d] = await Promise.all([api<Status>('/api/nginx'), api<Servers>('/api/nginx/servers')])
      setStatus(s)
      setDados(d)
    } catch (e) {
      setErro(msg(e))
    }
  }, [])

  useEffect(() => {
    void carregar()
    if (status?.fase !== 'provisionando') return
    const t = setInterval(carregar, 2000)
    return () => clearInterval(t)
  }, [carregar, status?.fase])

  // O erro do `nginx -t` é o texto do próprio nginx: mostrar inteiro, não
  // resumido — é ele que diz a linha e a diretiva recusada.
  const falha = erro ?? (status && !status.ok ? status.erro : null)

  return (
    <div className="flex flex-col gap-4">
      <Card>
        <CardHeader>
          <div className="flex flex-wrap items-start justify-between gap-3">
            <div>
              <CardTitle className="flex items-center gap-2">
                nginx
                {status && (
                  <Badge
                    variant={
                      status.fase === 'erro'
                        ? 'destructive'
                        : status.rodando
                          ? 'default'
                          : 'outline'
                    }
                  >
                    {status.fase === 'provisionando'
                      ? 'provisionando…'
                      : status.rodando
                        ? 'rodando'
                        : status.existe
                          ? 'parado'
                          : 'ausente'}
                  </Badge>
                )}
              </CardTitle>
              <CardDescription>
                {status?.imagem}
                {status?.aplicado_em && ` · última aplicação ${status.aplicado_em}`}
              </CardDescription>
            </div>
            <Button
              variant="outline"
              size="sm"
              onClick={async () => {
                try {
                  await api('/api/nginx/aplicar', { method: 'POST' })
                  toast.success('Config aplicada e recarregada')
                  setErro(null)
                } catch (e) {
                  setErro(msg(e))
                  toast.error('nginx recusou a config')
                }
                void carregar()
              }}
            >
              Reaplicar
            </Button>
          </div>
        </CardHeader>

        {falha && (
          <CardContent>
            <Alert variant="destructive">
              <AlertTitle>O nginx recusou a última config</AlertTitle>
              <AlertDescription>
                <pre className="mt-1 overflow-x-auto text-xs whitespace-pre-wrap">{falha}</pre>
              </AlertDescription>
            </Alert>
          </CardContent>
        )}
      </Card>

      {dados?.gerados.map((s) => (
        <CardServer key={s.id} server={s} conf={dados.conf_gerada} aoMudar={carregar} />
      ))}

      {dados?.do_usuario.map((s) => (
        <CardServer key={s.id} server={s} aoMudar={carregar} />
      ))}

      {dados && dados.do_usuario.length > 0 && (
        <Card>
          <CardHeader>
            <CardTitle className="text-base">Config gerada</CardTitle>
            <CardDescription>
              O que está dentro do container em{' '}
              <span className="font-mono">/etc/nginx/conf.d/user.conf</span>
            </CardDescription>
          </CardHeader>
          <CardContent>
            <pre className="bg-muted/50 max-h-72 overflow-auto rounded-md border p-3 font-mono text-xs">
              {dados.conf_usuario}
            </pre>
          </CardContent>
        </Card>
      )}

      <NovoServer aoCriar={carregar} />
    </div>
  )
}

function CardServer({
  server,
  conf,
  aoMudar,
}: {
  server: Server
  conf?: string
  aoMudar: () => void
}) {
  const [mostrarConf, setMostrarConf] = useState(false)

  async function remover(caminho: string, rotulo: string) {
    try {
      await api(caminho, { method: 'DELETE' })
      toast.success(rotulo)
      aoMudar()
    } catch (e) {
      toast.error('Não foi possível remover', { description: msg(e) })
    }
  }

  return (
    <Card>
      <CardHeader>
        <div className="flex flex-wrap items-start justify-between gap-3">
          <div>
            <CardTitle className="flex items-center gap-2 text-base">
              <span className="font-mono">{server.server_name}</span>
              {server.gerado && <Badge variant="secondary">gerado pelo Zeno</Badge>}
              {server.padrao && <Badge variant="outline">default_server</Badge>}
              {server.ssl && (
                <Badge variant={server.cert_pronto ? 'default' : 'outline'}>
                  {server.cert_pronto ? 'HTTPS ativo' : 'emitindo certificado…'}
                </Badge>
              )}
            </CardTitle>
            <CardDescription>
              {server.padrao
                ? 'Recebe todo request cujo Host não casa com nenhum outro vhost.'
                : server.gerado
                  ? 'Reescrito a cada sincronização — não editável aqui.'
                  : `${server.locations?.length ?? 0} location(s)`}
            </CardDescription>
          </div>
          <div className="flex gap-2">
            {conf && (
              <Button variant="ghost" size="sm" onClick={() => setMostrarConf(!mostrarConf)}>
                {mostrarConf ? 'Ocultar conf' : 'Ver conf'}
              </Button>
            )}
            {!server.gerado && (
              <Button
                variant="destructive"
                size="sm"
                onClick={() => remover(`/api/nginx/servers/${server.id}`, 'vhost removido')}
              >
                Remover vhost
              </Button>
            )}
          </div>
        </div>
      </CardHeader>

      <CardContent className="flex flex-col gap-3">
        {mostrarConf && conf && (
          <pre className="bg-muted/50 max-h-72 overflow-auto rounded-md border p-3 font-mono text-xs">
            {conf}
          </pre>
        )}

        {(server.locations ?? []).map((l) => (
          <div
            key={l.id}
            className="flex flex-wrap items-center gap-2 rounded-md border px-3 py-2 text-sm"
          >
            <span className="font-mono font-medium">{l.path}</span>
            <Badge variant="outline">{l.tipo}</Badge>
            <span className="text-muted-foreground min-w-0 flex-1 truncate font-mono text-xs">
              {l.alvo}
            </span>
            {l.dinamico && <Badge variant="secondary">resolver dinâmico</Badge>}
            {l.stream && <Badge variant="secondary">stream</Badge>}
            {!server.gerado && (
              <Button
                variant="ghost"
                size="sm"
                onClick={() => remover(`/api/nginx/locations/${l.id}`, 'location removida')}
              >
                remover
              </Button>
            )}
          </div>
        ))}

        {!server.gerado && (
          <>
            <Separator />
            <NovaLocation serverID={server.id} aoCriar={aoMudar} />
          </>
        )}
      </CardContent>
    </Card>
  )
}

function NovaLocation({ serverID, aoCriar }: { serverID: number; aoCriar: () => void }) {
  const [path, setPath] = useState('/')
  const [tipo, setTipo] = useState('proxy')
  const [alvo, setAlvo] = useState('')
  const [dinamico, setDinamico] = useState(true)
  const [stream, setStream] = useState(false)
  const [salvando, setSalvando] = useState(false)

  const placeholder =
    tipo === 'proxy'
      ? 'http://zeno-minha-app:3000'
      : tipo === 'redirect'
        ? 'https://novo-dominio.com$request_uri'
        : '/var/www/site'

  async function criar() {
    setSalvando(true)
    try {
      await api(`/api/nginx/servers/${serverID}/locations`, {
        method: 'POST',
        body: JSON.stringify({ path, tipo, alvo, dinamico: tipo === 'proxy' && dinamico, stream }),
      })
      toast.success('Location criada e nginx recarregado')
      setAlvo('')
      aoCriar()
    } catch (e) {
      toast.error('nginx recusou', { description: msg(e) })
    } finally {
      setSalvando(false)
    }
  }

  return (
    <div className="flex flex-col gap-3">
      <p className="text-xs font-medium">Nova location</p>

      <div className="grid gap-3 sm:grid-cols-[1fr_9rem_2fr]">
        <div className="flex flex-col gap-1.5">
          <Label htmlFor={`path-${serverID}`} className="text-xs">
            Path
          </Label>
          <Input
            id={`path-${serverID}`}
            value={path}
            onChange={(e) => setPath(e.target.value)}
            className="font-mono"
          />
        </div>

        <div className="flex flex-col gap-1.5">
          <Label className="text-xs">Tipo</Label>
          <Select value={tipo} onValueChange={setTipo}>
            <SelectTrigger>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="proxy">proxy</SelectItem>
              <SelectItem value="redirect">redirect</SelectItem>
              <SelectItem value="static">static</SelectItem>
            </SelectContent>
          </Select>
        </div>

        <div className="flex flex-col gap-1.5">
          <Label htmlFor={`alvo-${serverID}`} className="text-xs">
            {tipo === 'proxy' ? 'Upstream' : tipo === 'redirect' ? 'Destino' : 'Diretório'}
          </Label>
          <Input
            id={`alvo-${serverID}`}
            value={alvo}
            onChange={(e) => setAlvo(e.target.value)}
            placeholder={placeholder}
            className="font-mono"
          />
        </div>
      </div>

      {tipo === 'proxy' && (
        <div className="flex flex-col gap-2">
          <label className="flex items-start gap-3">
            <Switch checked={dinamico} onCheckedChange={setDinamico} />
            <span className="text-xs">
              <span className="font-medium">Resolver dinâmico</span>
              <span className="text-muted-foreground block">
                Resolve o upstream a cada request. Deixe ligado para container: o deploy recria com
                IP novo e sem isso o nginx serve 502 apontando pro IP antigo. Desligue para alvo
                fixo vindo do /etc/hosts, como host.docker.internal.
              </span>
            </span>
          </label>

          <label className="flex items-start gap-3">
            <Switch checked={stream} onCheckedChange={setStream} />
            <span className="text-xs">
              <span className="font-medium">Streaming (SSE / WebSocket)</span>
              <span className="text-muted-foreground block">
                Desliga o buffer. Sem isso, logs ao vivo travam esperando o buffer encher.
              </span>
            </span>
          </label>
        </div>
      )}

      <div>
        <Button size="sm" onClick={criar} disabled={salvando || !alvo || !path}>
          {salvando ? 'Validando…' : 'Adicionar e recarregar'}
        </Button>
      </div>
    </div>
  )
}

function NovoServer({ aoCriar }: { aoCriar: () => void }) {
  const [nome, setNome] = useState('')
  const [ssl, setSsl] = useState(true)
  const [salvando, setSalvando] = useState(false)

  async function criar() {
    setSalvando(true)
    try {
      await api('/api/nginx/servers', {
        method: 'POST',
        body: JSON.stringify({ server_name: nome, ssl }),
      })
      toast.success(
        ssl ? `vhost ${nome} criado — emitindo certificado` : `vhost ${nome} criado`,
      )
      setNome('')
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
        <CardTitle className="text-base">Novo vhost</CardTitle>
        <CardDescription>
          Escrito em <span className="font-mono">/etc/nginx/conf.d/user.conf</span>, separado do que
          o Zeno gera.
        </CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        <div className="flex flex-wrap items-end gap-3">
          <div className="flex min-w-64 flex-1 flex-col gap-1.5">
            <Label htmlFor="server_name" className="text-xs">
              server_name
            </Label>
            <Input
              id="server_name"
              value={nome}
              onChange={(e) => setNome(e.target.value)}
              placeholder="api.meudominio.com"
              className="font-mono"
            />
          </div>
          <Button onClick={criar} disabled={salvando || !nome}>
            {salvando ? 'Criando…' : 'Criar vhost'}
          </Button>
        </div>

        <label className="flex items-start gap-3">
          <Switch checked={ssl} onCheckedChange={setSsl} />
          <span className="text-xs">
            <span className="font-medium">SSL automático</span>
            <span className="text-muted-foreground block">
              Emite certificado via Let&apos;s Encrypt assim que o vhost for criado — precisa que
              server_name já resolva publicamente para este host, senão o desafio HTTP-01 nunca
              valida e a emissão fica tentando de novo a cada sincronização.
            </span>
          </span>
        </label>
      </CardContent>
    </Card>
  )
}
