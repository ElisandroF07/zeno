import { Check, ChevronsUpDown } from 'lucide-react'
import { useEffect, useState } from 'react'
import { toast } from 'sonner'
import { siGithub } from 'simple-icons'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Command,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
} from '@/components/ui/command'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'
import { api, msg } from '@/lib/api'
import { cn } from '@/lib/utils'

function GithubIcon({ className }: { className?: string }) {
  return (
    <svg role="img" viewBox="0 0 24 24" className={className} fill="currentColor" aria-hidden>
      <path d={siGithub.path} />
    </svg>
  )
}

type StatusGithub = {
  conectado: boolean
  configurado: boolean
  usuario: string
}

export type RepoGithub = {
  full_name: string
  private: boolean
  clone_url: string
  default_branch: string
}

// Hook simples, sem contexto global: só o card de conexão e o seletor de
// repositório precisam saber o status, e os dois já moram perto um do outro
// na tela de criar app.
export function useGithubStatus() {
  const [status, setStatus] = useState<StatusGithub | null>(null)

  const recarregar = () => {
    api<StatusGithub>('/api/github/status')
      .then(setStatus)
      .catch(() => setStatus({ conectado: false, configurado: false, usuario: '' }))
  }

  useEffect(recarregar, [])
  return { status, recarregar }
}

export function ConexaoGithub() {
  const { status, recarregar } = useGithubStatus()
  const [desconectando, setDesconectando] = useState(false)

  if (!status) return null

  if (!status.configurado) {
    // Não é erro do usuário — quem roda o Zeno ainda não configurou
    // ZENO_GITHUB_CLIENT_ID/SECRET. Sem isso o botão não tem pra onde ir.
    return null
  }

  async function desconectar() {
    setDesconectando(true)
    try {
      await api('/api/github/desconectar', { method: 'POST' })
      toast.success('GitHub desconectado')
      recarregar()
    } catch (e) {
      toast.error('Não foi possível desconectar', { description: msg(e) })
    } finally {
      setDesconectando(false)
    }
  }

  return (
    <div className="flex items-center gap-2">
      {status.conectado ? (
        <>
          <Badge variant="secondary" className="gap-1.5">
            <GithubIcon className="size-3.5" />
            conectado como <span className="font-mono">@{status.usuario}</span>
          </Badge>
          <Button variant="ghost" size="sm" onClick={desconectar} disabled={desconectando}>
            {desconectando ? 'Desconectando…' : 'Desconectar'}
          </Button>
        </>
      ) : (
        <Button
          variant="outline"
          size="sm"
          className="gap-1.5"
          onClick={() => {
            // Navegação de página inteira, não fetch: é o redirect real pra
            // tela de autorização do GitHub.
            window.location.href = '/api/github/login'
          }}
        >
          <GithubIcon className="size-3.5" />
          Conectar com GitHub
        </Button>
      )}
    </div>
  )
}

// SeletorRepoGithub é um combobox com busca — a lista de repositórios reais
// da conta conectada, exatamente como a Vercel mostra ao importar um
// projeto. Escolher aqui preenche repo_url e a branch padrão sozinho; nunca
// exige digitar nada.
export function SeletorRepoGithub({
  onEscolher,
}: {
  onEscolher: (repo: RepoGithub) => void
}) {
  const [aberto, setAberto] = useState(false)
  const [repos, setRepos] = useState<RepoGithub[] | null>(null)
  const [escolhido, setEscolhido] = useState<string | null>(null)
  const [carregando, setCarregando] = useState(false)

  useEffect(() => {
    if (!aberto || repos !== null) return
    setCarregando(true)
    api<RepoGithub[]>('/api/github/repos')
      .then(setRepos)
      .catch((e) => toast.error('Não foi possível listar os repositórios', { description: msg(e) }))
      .finally(() => setCarregando(false))
  }, [aberto, repos])

  return (
    <Popover open={aberto} onOpenChange={setAberto}>
      <PopoverTrigger asChild>
        <Button
          variant="outline"
          role="combobox"
          aria-expanded={aberto}
          className="w-full justify-between font-mono text-xs font-normal"
        >
          {escolhido ?? 'Escolher repositório do GitHub…'}
          <ChevronsUpDown className="ml-2 size-4 shrink-0 opacity-50" />
        </Button>
      </PopoverTrigger>
      <PopoverContent className="w-(--radix-popover-trigger-width) p-0">
        <Command>
          <CommandInput placeholder="Buscar repositório…" />
          <CommandList>
            {carregando && <div className="text-muted-foreground p-3 text-xs">Carregando…</div>}
            <CommandEmpty>Nenhum repositório encontrado.</CommandEmpty>
            <CommandGroup>
              {repos?.map((repo) => (
                <CommandItem
                  key={repo.full_name}
                  value={repo.full_name}
                  onSelect={() => {
                    setEscolhido(repo.full_name)
                    setAberto(false)
                    onEscolher(repo)
                  }}
                  className="font-mono text-xs"
                >
                  <Check
                    className={cn(
                      'mr-2 size-4',
                      escolhido === repo.full_name ? 'opacity-100' : 'opacity-0',
                    )}
                  />
                  {repo.full_name}
                  {repo.private && (
                    <Badge variant="outline" className="ml-auto text-[10px]">
                      privado
                    </Badge>
                  )}
                </CommandItem>
              ))}
            </CommandGroup>
          </CommandList>
        </Command>
      </PopoverContent>
    </Popover>
  )
}
