import { useCallback, useEffect, useState } from 'react'
import { Toaster } from 'sonner'
import { type Secao, AppSidebar } from '@/AppSidebar'
import { Apps } from '@/Apps'
import { Catalogo } from '@/Catalogo'
import { NginxEditor } from '@/NginxEditor'
import { Servicos, type Servico } from '@/Servicos'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Separator } from '@/components/ui/separator'
import { SidebarInset, SidebarProvider, SidebarTrigger } from '@/components/ui/sidebar'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { api, msg } from '@/lib/api'

type Container = {
  id: string
  nome: string
  imagem: string
  estado: string
  status: string
  portas: string[] | null
  zeno: boolean
  tipo?: string
}

const titulos: Record<Secao, string> = {
  apps: 'Apps',
  servicos: 'Serviços',
  catalogo: 'Catálogo',
  nginx: 'nginx',
  containers: 'Containers',
}

export default function App() {
  const [secao, setSecao] = useState<Secao>('apps')
  const [categoriaAtiva, setCategoriaAtiva] = useState<string | null>(null)
  const [categorias, setCategorias] = useState<string[]>([])
  const [servicos, setServicos] = useState<Servico[]>([])

  const carregarServicos = useCallback(() => {
    api<Servico[]>('/api/services').then(setServicos).catch(() => {})
  }, [])

  useEffect(() => carregarServicos(), [carregarServicos])

  return (
    <SidebarProvider>
      <AppSidebar
        ativo={secao}
        onNavegar={setSecao}
        contagemServicos={servicos.length}
        categorias={categorias}
        categoriaAtiva={categoriaAtiva}
        onCategoria={setCategoriaAtiva}
      />
      <SidebarInset>
        <header className="flex h-14 shrink-0 items-center gap-2 border-b px-4">
          <SidebarTrigger />
          <Separator orientation="vertical" className="h-4!" />
          <h1 className="text-sm font-medium">
            {titulos[secao]}
            {secao === 'catalogo' && categoriaAtiva && (
              <span className="text-muted-foreground"> / {categoriaAtiva}</span>
            )}
          </h1>
        </header>

        <main className="flex flex-1 flex-col gap-6 p-6">
          {secao === 'apps' && <Apps />}

          {secao === 'servicos' && <Servicos servicos={servicos} aoMudar={carregarServicos} />}

          {secao === 'catalogo' && (
            <Catalogo
              aoCriar={carregarServicos}
              filtroCategoria={categoriaAtiva}
              aoCarregarCategorias={setCategorias}
            />
          )}

          {secao === 'nginx' && <NginxEditor />}

          {secao === 'containers' && <Containers />}
        </main>
      </SidebarInset>
      <Toaster position="bottom-right" />
    </SidebarProvider>
  )
}

function Containers() {
  const [containers, setContainers] = useState<Container[]>([])
  const [erro, setErro] = useState<string | null>(null)
  const [carregando, setCarregando] = useState(true)

  const carregar = useCallback(async () => {
    setCarregando(true)
    try {
      setContainers(await api<Container[]>('/api/containers'))
      setErro(null)
    } catch (e) {
      setErro(msg(e))
    } finally {
      setCarregando(false)
    }
  }, [])

  useEffect(() => {
    void carregar()
  }, [carregar])

  return (
    <Card>
      <CardHeader>
        <div className="flex items-start justify-between">
          <div>
            <CardTitle>Docker</CardTitle>
            <CardDescription>
              {erro
                ? 'Sem conexão com o daemon'
                : `${containers.length} container${containers.length === 1 ? '' : 's'} neste host`}
            </CardDescription>
          </div>
          <Button variant="outline" size="sm" onClick={carregar} disabled={carregando}>
            {carregando ? 'Carregando…' : 'Atualizar'}
          </Button>
        </div>
      </CardHeader>
      <CardContent>
        {erro ? (
          <div className="text-sm">
            <p className="text-destructive font-medium">{erro}</p>
            <p className="text-muted-foreground mt-1">
              O Docker Desktop (ou o serviço dockerd) precisa estar rodando.
            </p>
          </div>
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Nome</TableHead>
                <TableHead>Imagem</TableHead>
                <TableHead>Estado</TableHead>
                <TableHead>Portas</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {containers.map((c) => (
                <TableRow key={c.id}>
                  <TableCell className="font-medium">
                    <div className="flex items-center gap-2">
                      {c.nome}
                      {c.zeno && <Badge variant="secondary">{c.tipo ?? 'zeno'}</Badge>}
                    </div>
                    <span className="text-muted-foreground font-mono text-xs">{c.id}</span>
                  </TableCell>
                  <TableCell className="text-muted-foreground max-w-56 truncate text-sm">
                    {c.imagem}
                  </TableCell>
                  <TableCell>
                    <Badge variant={c.estado === 'running' ? 'default' : 'outline'}>
                      {c.estado}
                    </Badge>
                    <div className="text-muted-foreground mt-1 text-xs">{c.status}</div>
                  </TableCell>
                  <TableCell className="font-mono text-xs">{c.portas?.join(' ') || '—'}</TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </CardContent>
    </Card>
  )
}
