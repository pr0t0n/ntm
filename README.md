# NTM — Network Threat Monitoring

Plataforma de **inventário de ativos de rede** (CMDB) que descobre o que existe
na rede interna via `nmap`, enumeração de DNS e leitura da tabela ARP, e
apresenta tudo em um Dashboard, um Mapa de Rede e um CMDB pesquisável.

> Ferramenta de descoberta de ativos para **a sua própria rede**. Execute apenas
> em redes que você é autorizado a inventariar. O agente só escaneia as faixas
> que você configurar explicitamente.

## Arquitetura

```
┌─────────────┐   HTTPS/TLS + token    ┌──────────────┐        ┌────────────┐
│  NTM Agent  │ ─────────────────────► │ NTM Platform │ ─────► │ PostgreSQL │
│  (Go+nmap)  │   POST /scan-results   │   (Go API)   │        └────────────┘
│  Linux host │                        │  + Web UI    │
└─────────────┘                        └──────┬───────┘
   nmap top-100 TCP                           │ serve
   DNS enum (PTR/A)                      ┌─────▼──────────────────────┐
   ARP (/proc/net/arp)                  │ Dashboard · Mapa · CMDB     │
                                        └─────────────────────────────┘
```

- **Agente (Go)** — `agent/`. Instalado no Linux. Na instalação recebe o **IP/URL
  da plataforma** e o **token de instalação**. Faz enrollment, obtém um token de
  agente, e em cada ciclo: orquestra `nmap` (host up/down + top 100 portas TCP +
  versão de serviço), faz enumeração de DNS (PTR → A) e lê a tabela ARP, e envia
  o relatório por **HTTPS over TLS**.
- **Plataforma (Go)** — `platform/`. API HTTPS/TLS, classificação de ativos
  (computador / servidor / equipamento / impressora), persistência em Postgres e
  a UI web embutida.
- **Banco** — PostgreSQL.

## Subir com Docker (stack completa)

```bash
cp .env.example .env            # ajuste o NTM_INSTALL_TOKEN
docker compose up -d --build
```

Acesse a UI em **https://localhost:8443** (certificado self-signed — aceite o
aviso). A plataforma gera o certificado TLS no primeiro boot.

O serviço `agent` do compose já sobe configurado com o IP da plataforma e o
token, e escaneia a rede do compose (`172.28.0.0/24`) para você ver o pipeline
funcionando ponta a ponta.

### Portas TCP verificadas
`nmap --top-ports 100` cobre as 100 portas TCP mais comuns, incluindo
20, 21, 22, 23, 25, 53, 80, 110, 135, 137, 138, 139, 143, 443, 445, 993, 995,
1433, 3306, 3389, 5900, 8080, 8443, entre outras.

## Instalar o agente num host Linux real (fora do Docker)

```bash
cd agent
go build -o ntm-agent .
sudo ./install.sh
# Informe quando solicitado:
#   Platform URL: https://<IP-DA-PLATAFORMA>:8443
#   Installation token: <token de instalação>
```

Isso instala um serviço systemd (`ntm-agent`). Configure as faixas a escanear em
`/opt/ntm/agent.env` (`NTM_RANGES`, padrão `10.0.0.0/8,172.16.0.0/12,192.168.0.0/16`).
Logs: `journalctl -u ntm-agent -f`.

## Telas

- **Dashboard** — total de hosts (divididos em Computadores / Servidores /
  Equipamentos), Sistemas Operacionais, Aplicações (top 10) e portas abertas.
- **Mapa de Rede** — interligações dos sistemas, agrupados por sub-rede, até a
  borda (Internet). Clique num host para abri-lo no CMDB.
- **CMDB** — busca por **IP, Hostname, Aplicação ou Porta**. Clique num ativo
  para ver detalhes (portas, serviços, MAC, SO, último contato).

## API (resumo)

| Método | Rota | Descrição |
|-------|------|-----------|
| POST | `/api/v1/enroll` | Enrollment do agente (install token → agent token) |
| POST | `/api/v1/scan-results` | Ingestão de relatório (Bearer agent token) |
| GET  | `/api/v1/dashboard` | Agregados do dashboard |
| GET  | `/api/v1/hosts?field=ip\|hostname\|app\|port&q=` | Busca CMDB |
| GET  | `/api/v1/hosts/{id}` | Detalhe de um ativo |
| GET  | `/api/v1/network-map` | Nós e arestas do mapa |
| GET  | `/api/v1/healthz` | Health check |

## Configuração (variáveis de ambiente)

**Plataforma:** `NTM_LISTEN`, `NTM_DATABASE_URL`, `NTM_INSTALL_TOKEN`,
`NTM_TLS_CERT`, `NTM_TLS_KEY`.

**Agente:** `NTM_PLATFORM_URL`, `NTM_INSTALL_TOKEN`, `NTM_RANGES`,
`NTM_SCAN_INTERVAL`, `NTM_NMAP_FLAGS`, `NTM_DNS_RESOLVER`, `NTM_TLS_INSECURE`,
`NTM_STATE_PATH`, `NTM_AGENT_NAME`.

## Notas de produção

- Troque o certificado self-signed por um emitido por uma CA confiável (monte
  sobre `/certs`) e deixe `NTM_TLS_INSECURE=false` no agente.
- Use um `NTM_INSTALL_TOKEN` forte e rotacione-o; cada agente recebe um token
  próprio no enrollment.
- Para detecção de SO (`nmap -O`) e SYN scan (`-sS`) o agente precisa de
  `CAP_NET_RAW`/`CAP_NET_ADMIN` (já concedidos no compose e no unit systemd).
