import { useEffect, useState } from 'react'
import { toast } from 'sonner'
import { ServiceIcon } from '@/ServiceIcon'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { api, msg } from '@/lib/api'

export type CampoEnv = {
  key: string
  label: string
  descricao: string
  padrao: string
  tipo: string
  gerar: string
  obrigatorio: boolean
}

export type Preset = {
  id: string
  nome: string
  categoria: string
  descricao: string
  icone: string
  image: string
  porta: number
  env: CampoEnv[] | null
  admin_ui?: { porta: number; nome: string }
  injeta: Record<string, string> | null
}

export function Catalogo({
  aoCriar,
  filtroCategoria,
  aoCarregarCategorias,
}: {
  aoCriar: () => void
  filtroCategoria: string | null
  // A sidebar precisa da lista de categorias antes de o usuário abrir a
  // aba — sem isso a sub-navegação só apareceria depois do primeiro fetch.
  aoCarregarCategorias: (cs: string[]) => void
}) {
  const [presets, setPresets] = useState<Preset[]>([])
  const [escolhido, setEscolhido] = useState<Preset | null>(null)

  useEffect(() => {
    api<Preset[]>('/api/presets')
      .then((ps) => {
        setPresets(ps)
        aoCarregarCategorias([...new Set(ps.map((p) => p.categoria))])
      })
      .catch((e) => toast.error('Não foi possível carregar o catálogo', { description: msg(e) }))
    // aoCarregarCategorias vem de um useState setter no pai: estável entre
    // renders, não precisa entrar nas deps e re-disparar o fetch.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  const categorias = [...new Set(presets.map((p) => p.categoria))].filter(
    (c) => !filtroCategoria || c === filtroCategoria,
  )

  return (
    <div className="flex flex-col gap-6">
      {categorias.map((categoria) => (
        <section key={categoria}>
          <h2 className="text-muted-foreground mb-3 text-sm font-medium">{categoria}</h2>
          <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
            {presets
              .filter((p) => p.categoria === categoria)
              .map((p) => (
                <Card key={p.id} className="flex flex-col">
                  <CardHeader>
                    <div className="flex items-start gap-3">
                      <ServiceIcon slug={p.icone} size={28} />
                      <div className="min-w-0 flex-1">
                        <CardTitle className="text-base">{p.nome}</CardTitle>
                        <CardDescription className="font-mono text-xs">{p.image}</CardDescription>
                      </div>
                    </div>
                  </CardHeader>
                  <CardContent className="flex flex-1 flex-col justify-between gap-4">
                    <p className="text-muted-foreground text-sm">{p.descricao}</p>
                    <div className="flex items-center justify-between">
                      {p.admin_ui ? (
                        <Badge variant="secondary">{p.admin_ui.nome}</Badge>
                      ) : (
                        <span />
                      )}
                      <Button size="sm" onClick={() => setEscolhido(p)}>
                        Adicionar
                      </Button>
                    </div>
                  </CardContent>
                </Card>
              ))}
          </div>
        </section>
      ))}

      <DialogNovoServico
        preset={escolhido}
        aoFechar={() => setEscolhido(null)}
        aoCriar={() => {
          setEscolhido(null)
          aoCriar()
        }}
      />
    </div>
  )
}

function DialogNovoServico({
  preset,
  aoFechar,
  aoCriar,
}: {
  preset: Preset | null
  aoFechar: () => void
  aoCriar: () => void
}) {
  const [nome, setNome] = useState('')
  const [env, setEnv] = useState<Record<string, string>>({})
  const [erro, setErro] = useState<string | null>(null)
  const [salvando, setSalvando] = useState(false)

  // Cada preset abre o formulário limpo, com os padrões dele.
  useEffect(() => {
    if (!preset) return
    setNome(preset.id)
    setEnv(Object.fromEntries((preset.env ?? []).map((c) => [c.key, c.padrao])))
    setErro(null)
  }, [preset])

  if (!preset) return null

  async function criar() {
    setSalvando(true)
    setErro(null)
    try {
      await api('/api/services', {
        method: 'POST',
        body: JSON.stringify({ preset_id: preset!.id, nome, env }),
      })
      toast.success(`${preset!.nome} no ar`, { description: `container zeno-${nome}` })
      aoCriar()
    } catch (e) {
      setErro(msg(e))
    } finally {
      setSalvando(false)
    }
  }

  return (
    <Dialog open onOpenChange={(aberto) => !aberto && aoFechar()}>
      <DialogContent className="max-h-[85vh] overflow-y-auto sm:max-w-lg">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <ServiceIcon slug={preset.icone} size={20} />
            Novo {preset.nome}
          </DialogTitle>
          <DialogDescription>
            Sobe <span className="font-mono">{preset.image}</span> na rede <code>zeno</code>.
          </DialogDescription>
        </DialogHeader>

        <div className="flex flex-col gap-4 py-2">
          <div className="flex flex-col gap-2">
            <Label htmlFor="nome">Nome do serviço</Label>
            <Input
              id="nome"
              value={nome}
              onChange={(e) => setNome(e.target.value)}
              placeholder="meu-banco"
            />
            <p className="text-muted-foreground text-xs">
              Vira o container <span className="font-mono">zeno-{nome || 'nome'}</span> e o endereço
              das apps na rede. Minúsculas, números e hífen.
            </p>
          </div>

          {(preset.env ?? []).map((campo) => (
            <div key={campo.key} className="flex flex-col gap-2">
              <Label htmlFor={campo.key}>
                {campo.label || campo.key}
                {campo.obrigatorio && <span className="text-destructive ml-1">*</span>}
              </Label>
              <Input
                id={campo.key}
                type={campo.tipo === 'senha' ? 'password' : 'text'}
                value={env[campo.key] ?? ''}
                onChange={(e) => setEnv({ ...env, [campo.key]: e.target.value })}
                placeholder={campo.gerar === 'senha' ? 'em branco = gerada automaticamente' : ''}
              />
              {campo.descricao && (
                <p className="text-muted-foreground text-xs">{campo.descricao}</p>
              )}
            </div>
          ))}

          {preset.injeta && Object.keys(preset.injeta).length > 0 && (
            <div className="bg-muted/50 rounded-md border p-3">
              <p className="mb-1 text-xs font-medium">Injetado nas apps vinculadas</p>
              <p className="text-muted-foreground font-mono text-xs">
                {Object.keys(preset.injeta).join(', ')}
              </p>
            </div>
          )}

          {erro && (
            <Alert variant="destructive">
              <AlertDescription className="text-xs whitespace-pre-wrap">{erro}</AlertDescription>
            </Alert>
          )}
        </div>

        <DialogFooter>
          <Button variant="outline" onClick={aoFechar} disabled={salvando}>
            Cancelar
          </Button>
          <Button onClick={criar} disabled={salvando || !nome}>
            {salvando ? 'Subindo…' : 'Criar serviço'}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
