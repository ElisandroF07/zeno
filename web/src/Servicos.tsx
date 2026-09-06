import { useState } from 'react'
import { toast } from 'sonner'
import type { Preset } from '@/Catalogo'
import { ServiceIcon } from '@/ServiceIcon'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Separator } from '@/components/ui/separator'
import { api, msg } from '@/lib/api'

export type Servico = {
  id: number
  preset_id: string
  nome: string
  preset?: Preset
  env: Record<string, string> | null
  injetadas: Record<string, string> | null
  estado: string
  status: string
  admin_url?: string
}

export function Servicos({
  servicos,
  aoMudar,
}: {
  servicos: Servico[]
  aoMudar: () => void
}) {
  if (servicos.length === 0) {
    return (
      <Card>
        <CardContent className="text-muted-foreground py-10 text-center text-sm">
          Nenhum serviço ainda. Escolha um no catálogo.
        </CardContent>
      </Card>
    )
  }

  return (
    <div className="flex flex-col gap-3">
      {servicos.map((s) => (
        <CardServico key={s.id} servico={s} aoMudar={aoMudar} />
      ))}
    </div>
  )
}

function CardServico({ servico, aoMudar }: { servico: Servico; aoMudar: () => void }) {
  const [ocupado, setOcupado] = useState(false)
  const rodando = servico.estado === 'running'

  async function acao(caminho: string, metodo: string, rotulo: string) {
    setOcupado(true)
    try {
      await api(caminho, { method: metodo })
      toast.success(`${servico.nome}: ${rotulo}`)
      aoMudar()
    } catch (e) {
      toast.error(`Falhou: ${rotulo}`, { description: msg(e) })
    } finally {
      setOcupado(false)
    }
  }

  return (
    <Card>
      <CardHeader>
        <div className="flex flex-wrap items-start justify-between gap-3">
          <div className="flex items-start gap-3">
            <ServiceIcon slug={servico.preset?.icone} size={28} />
            <div>
              <CardTitle className="flex items-center gap-2 text-base">
                {servico.nome}
                <Badge
                  variant={
                    rodando ? 'default' : servico.estado === 'ausente' ? 'destructive' : 'outline'
                  }
                >
                  {servico.estado}
                </Badge>
              </CardTitle>
              <CardDescription className="font-mono text-xs">
                {servico.preset?.image} · {servico.status}
              </CardDescription>
            </div>
          </div>

          <div className="flex flex-wrap gap-2">
            {servico.admin_url && rodando && (
              <Button size="sm" variant="secondary" asChild>
                <a href={servico.admin_url} target="_blank" rel="noreferrer">
                  {servico.preset?.admin_ui?.nome ?? 'Painel'} ↗
                </a>
              </Button>
            )}
            <Button
              size="sm"
              variant="outline"
              disabled={ocupado}
              onClick={() =>
                acao(
                  `/api/services/${servico.nome}/${rodando ? 'stop' : 'start'}`,
                  'POST',
                  rodando ? 'parado' : 'iniciado',
                )
              }
            >
              {rodando ? 'Parar' : 'Iniciar'}
            </Button>
            <Button
              size="sm"
              variant="outline"
              disabled={ocupado || !rodando}
              onClick={() => acao(`/api/services/${servico.nome}/restart`, 'POST', 'reiniciado')}
            >
              Reiniciar
            </Button>
            <Button
              size="sm"
              variant="destructive"
              disabled={ocupado}
              onClick={() => acao(`/api/services/${servico.nome}`, 'DELETE', 'removido')}
            >
              Remover
            </Button>
          </div>
        </div>
      </CardHeader>

      <CardContent className="flex flex-col gap-4">
        {servico.injetadas && Object.keys(servico.injetadas).length > 0 && (
          <div>
            <p className="mb-2 text-xs font-medium">Variáveis de conexão</p>
            <div className="flex flex-col gap-1">
              {Object.entries(servico.injetadas).map(([k, v]) => (
                <LinhaCopiavel key={k} chave={k} valor={v} />
              ))}
            </div>
          </div>
        )}

        {servico.env && Object.keys(servico.env).length > 0 && (
          <>
            <Separator />
            <div>
              <p className="mb-2 text-xs font-medium">Configuração</p>
              <div className="flex flex-col gap-1">
                {Object.entries(servico.env).map(([k, v]) => (
                  <LinhaCopiavel key={k} chave={k} valor={v} segredo={/PASS|SECRET|KEY/i.test(k)} />
                ))}
              </div>
            </div>
          </>
        )}
      </CardContent>
    </Card>
  )
}

function LinhaCopiavel({
  chave,
  valor,
  segredo = false,
}: {
  chave: string
  valor: string
  segredo?: boolean
}) {
  const [revelado, setRevelado] = useState(false)
  const mostrar = !segredo || revelado

  return (
    <div className="group hover:bg-muted/50 flex items-center gap-2 rounded px-2 py-1 font-mono text-xs">
      <span className="text-muted-foreground shrink-0">{chave}</span>
      <span className="min-w-0 flex-1 truncate">{mostrar ? valor : '••••••••••••'}</span>
      {segredo && (
        <button
          type="button"
          className="text-muted-foreground hover:text-foreground shrink-0 opacity-0 group-hover:opacity-100"
          onClick={() => setRevelado(!revelado)}
        >
          {revelado ? 'ocultar' : 'ver'}
        </button>
      )}
      <button
        type="button"
        className="text-muted-foreground hover:text-foreground shrink-0 opacity-0 group-hover:opacity-100"
        onClick={() => {
          void navigator.clipboard.writeText(valor)
          toast.success(`${chave} copiado`)
        }}
      >
        copiar
      </button>
    </div>
  )
}
