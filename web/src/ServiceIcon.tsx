import {
  siApachecouchdb,
  siApachekafka,
  siBaserow,
  siBitwarden,
  siCalibreweb,
  siChatwoot,
  siClickhouse,
  siDocker,
  siEclipsemosquitto,
  siExcalidraw,
  siGhost,
  siGitea,
  siGrafana,
  siHomepage,
  siJellyfin,
  siListmonk,
  siMariadb,
  siMeilisearch,
  siMetabase,
  siMinio,
  siMongodb,
  siMysql,
  siN8n,
  siNextcloud,
  siNginx,
  siPaperlessngx,
  siPlex,
  siPortainer,
  siPostgresql,
  siPrometheus,
  siQdrant,
  siRabbitmq,
  siRedis,
  siSurrealdb,
  siTrello,
  siUptimekuma,
  siVikunja,
  siWordpress,
  type SimpleIcon,
} from 'simple-icons'

// Import nomeado, não lookup dinâmico no pacote inteiro: o simple-icons tem
// milhares de ícones e só estes entram no bundle. Alguns presets (Typesense,
// NocoDB, Chroma, Memcached, PhotoPrism, Stalwart) não têm marca no
// simple-icons — caem no fallback genérico abaixo, o que é honesto: melhor
// sem logo do que com o logo errado.
const icones: Record<string, SimpleIcon> = {
  apachecouchdb: siApachecouchdb,
  apachekafka: siApachekafka,
  baserow: siBaserow,
  bitwarden: siBitwarden,
  calibreweb: siCalibreweb,
  chatwoot: siChatwoot,
  clickhouse: siClickhouse,
  docker: siDocker,
  eclipsemosquitto: siEclipsemosquitto,
  excalidraw: siExcalidraw,
  ghost: siGhost,
  gitea: siGitea,
  grafana: siGrafana,
  homepage: siHomepage,
  jellyfin: siJellyfin,
  listmonk: siListmonk,
  mariadb: siMariadb,
  meilisearch: siMeilisearch,
  metabase: siMetabase,
  minio: siMinio,
  mongodb: siMongodb,
  mysql: siMysql,
  n8n: siN8n,
  nextcloud: siNextcloud,
  nginx: siNginx,
  paperlessngx: siPaperlessngx,
  plex: siPlex,
  portainer: siPortainer,
  postgresql: siPostgresql,
  prometheus: siPrometheus,
  qdrant: siQdrant,
  rabbitmq: siRabbitmq,
  redis: siRedis,
  surrealdb: siSurrealdb,
  trello: siTrello,
  uptimekuma: siUptimekuma,
  vikunja: siVikunja,
  wordpress: siWordpress,
}

export function ServiceIcon({ slug, size = 24 }: { slug?: string; size?: number }) {
  const icone = slug ? icones[slug] : undefined

  if (!icone) {
    return (
      <div
        className="bg-muted text-muted-foreground grid shrink-0 place-items-center rounded-md font-mono text-xs"
        style={{ width: size, height: size }}
        aria-hidden
      >
        ?
      </div>
    )
  }

  return (
    <svg
      role="img"
      aria-label={icone.title}
      viewBox="0 0 24 24"
      width={size}
      height={size}
      className="shrink-0"
      fill={`#${icone.hex}`}
    >
      <path d={icone.path} />
    </svg>
  )
}
