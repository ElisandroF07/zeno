import {
  Boxes,
  LayoutGrid,
  type LucideIcon,
  Network,
  Rocket,
  Server,
  ShoppingBag,
} from 'lucide-react'
import {
  Sidebar,
  SidebarContent,
  SidebarGroup,
  SidebarGroupContent,
  SidebarHeader,
  SidebarMenu,
  SidebarMenuBadge,
  SidebarMenuButton,
  SidebarMenuItem,
  SidebarMenuSub,
  SidebarMenuSubButton,
  SidebarMenuSubItem,
} from '@/components/ui/sidebar'

export type Secao = 'apps' | 'servicos' | 'catalogo' | 'nginx' | 'containers'

const itens: { id: Secao; label: string; icon: LucideIcon }[] = [
  { id: 'apps', label: 'Apps', icon: Rocket },
  { id: 'servicos', label: 'Serviços', icon: Server },
  { id: 'catalogo', label: 'Catálogo', icon: ShoppingBag },
  { id: 'nginx', label: 'nginx', icon: Network },
  { id: 'containers', label: 'Containers', icon: Boxes },
]

export function AppSidebar({
  ativo,
  onNavegar,
  contagemServicos,
  categorias,
  categoriaAtiva,
  onCategoria,
}: {
  ativo: Secao
  onNavegar: (s: Secao) => void
  contagemServicos: number
  categorias: string[]
  categoriaAtiva: string | null
  onCategoria: (c: string | null) => void
}) {
  return (
    <Sidebar>
      <SidebarHeader className="px-3 py-3">
        <div className="flex items-center gap-2">
          <div className="bg-primary text-primary-foreground grid size-7 shrink-0 place-items-center rounded-md">
            <LayoutGrid className="size-4" />
          </div>
          <div>
            <p className="text-sm leading-none font-semibold">Zeno</p>
            <p className="text-muted-foreground text-xs">este host</p>
          </div>
        </div>
      </SidebarHeader>

      <SidebarContent>
        <SidebarGroup>
          <SidebarGroupContent>
            <SidebarMenu>
              {itens.map((item) => (
                <SidebarMenuItem key={item.id}>
                  <SidebarMenuButton
                    isActive={ativo === item.id}
                    onClick={() => onNavegar(item.id)}
                  >
                    <item.icon />
                    <span>{item.label}</span>
                  </SidebarMenuButton>
                  {item.id === 'servicos' && contagemServicos > 0 && (
                    <SidebarMenuBadge>{contagemServicos}</SidebarMenuBadge>
                  )}

                  {/* Categorias do catálogo como sub-navegação — só aparecem
                      com a seção ativa, senão é ruído para quem não pediu. */}
                  {item.id === 'catalogo' && ativo === 'catalogo' && (
                    <SidebarMenuSub>
                      <SidebarMenuSubItem>
                        <SidebarMenuSubButton
                          isActive={categoriaAtiva === null}
                          onClick={() => onCategoria(null)}
                        >
                          Todas
                        </SidebarMenuSubButton>
                      </SidebarMenuSubItem>
                      {categorias.map((c) => (
                        <SidebarMenuSubItem key={c}>
                          <SidebarMenuSubButton
                            isActive={categoriaAtiva === c}
                            onClick={() => onCategoria(c)}
                          >
                            {c}
                          </SidebarMenuSubButton>
                        </SidebarMenuSubItem>
                      ))}
                    </SidebarMenuSub>
                  )}
                </SidebarMenuItem>
              ))}
            </SidebarMenu>
          </SidebarGroupContent>
        </SidebarGroup>
      </SidebarContent>
    </Sidebar>
  )
}
