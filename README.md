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
  agente, e em cada ciclo executa a descoberta em **4 fases**, gravando o
  resultado de cada fase na plataforma por **HTTPS over TLS** à medida que avança:
  1. **Ping** — ICMP echo nativo do agente (sem nmap) em todos os endereços das
     faixas; cada host que responde é gravado na hora (MAC via tabela ARP). O
     `nmap` só é executado depois que o ping termina.
  2. **Hostname** — DNS reverso (PTR → A) dos hosts ativos.
  3. **Portas** — top 100 portas TCP dos hosts ativos, em lotes de 64.
  4. **SO e plataformas publicadas** — `nmap -O -sV` nas portas abertas
     (sistema operacional + produto/versão de cada serviço).

  O Dashboard mostra a fase atual, o tempo total, o tempo da fase e a quantidade
  de hosts encontrados.
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
token, e escaneia as três redes internas RFC1918 (`10.0.0.0/8`, `172.16.0.0/12`,
`192.168.0.0/16`), que incluem a rede do compose (`172.28.0.0/24`).

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
- **Varredura** — seleciona o agente, mostra o **IP do agente** e uma **sugestão
  de range** (o /24 do IP), com o campo de range **editável** para o usuário
  informar a faixa que quer varrer (CIDR, separado por vírgula). Tem o progresso
  das 4 fases (tempo total, tempo da fase, hosts encontrados), o **download do
  agente** para instalação e a **remoção do agente** selecionado.

## API (resumo)

| Método | Rota | Descrição |
|-------|------|-----------|
| POST | `/api/v1/enroll` | Enrollment do agente (install token → agent token) |
| POST | `/api/v1/scan-phase` | Ingestão de um lote de uma fase: `ping` \| `hostname` \| `ports` \| `detect` (Bearer agent token) |
| POST | `/api/v1/agent/status` | Estado do scan e fase atual (Bearer agent token) |
| POST | `/api/v1/scan-results` | Ingestão de relatório completo (legado) |
| GET  | `/api/v1/dashboard` | Agregados do dashboard |
| GET  | `/api/v1/hosts?field=ip\|hostname\|app\|port&q=` | Busca CMDB |
| GET  | `/api/v1/hosts/{id}` | Detalhe de um ativo |
| GET  | `/api/v1/network-map` | Nós e arestas do mapa |
| GET  | `/api/v1/agents` | Lista de agentes (com `ip` e `suggested_range`) |
| POST | `/api/v1/agents/{id}/run` | Dispara varredura; corpo `{"range":"CIDR,..."}` (vazio = ranges do agente) |
| POST | `/api/v1/agents/{id}/stop` | Interrompe a varredura em andamento |
| DELETE | `/api/v1/agents/{id}` | Remove o agente (mantém os ativos descobertos) |
| POST | `/api/v1/reset` | Apaga todo o inventário (hosts, portas, DNS) |
| GET  | `/ntm_agent.py` | Download do agente para instalação |
| GET  | `/api/v1/healthz` | Health check |

## Configuração (variáveis de ambiente)

**Plataforma:** `NTM_LISTEN`, `NTM_DATABASE_URL`, `NTM_INSTALL_TOKEN`,
`NTM_TLS_CERT`, `NTM_TLS_KEY`.

**Agente:** `NTM_PLATFORM_URL`, `NTM_INSTALL_TOKEN`, `NTM_RANGES`,
`NTM_SCAN_INTERVAL`, `NTM_DNS_RESOLVER`, `NTM_TLS_INSECURE`, `NTM_STATE_PATH`,
`NTM_AGENT_NAME`, o ping da fase 1 (`NTM_PING_RATE`, pacotes/s, padrão 1000;
`NTM_PING_TIMEOUT`, padrão 2s), o port scan em Python (`NTM_PORT_WORKERS`,
`NTM_CONNECT_TIMEOUT`) e `NTM_EXCLUDE` (redes/IPs a nunca inventariar, CIDR
separado por vírgula). O agente e a plataforma são sempre excluídos; no compose
o default de `NTM_EXCLUDE` é a rede do Docker (`172.28.0.0/16`).

## Resolução de problemas

**"Rodei nmap manualmente e achei muita coisa, mas a plataforma mostra pouco/nada"**
- Verifique o **escopo** do agente: `docker exec ntm-agent sh -c 'echo $NTM_RANGES'`.
  O `.env` local pode estar limitando as faixas. O default escaneia as três redes
  privadas.
- O agente faz **scan SYN (`-sS`)**, que exige `CAP_NET_RAW` (concedido no compose
  e no unit systemd). Sem ele, caia para connect scan: `NTM_PORT_FLAGS="-sT --top-ports 100 --open -T4 -n --host-timeout 60s"`.
  O ping da fase 1 (socket ICMP) e a fase 4 (`-O`) também exigem root/`CAP_NET_RAW`.

**Docker Desktop no macOS/Windows distorce o scan de faixas off-subnet.** A VM do
Docker faz NAT de qualquer endereço fora da rede do container (ex.: todo
`192.168.x` / `10.x`) aparecer "up", criando **hosts fantasma** cujas portas
penduram o scanner. Isso **não** acontece num host Linux real na rede alvo. Para
um teste fiel, instale o agente num host Linux da sua rede (veja acima) ou, no
lab, escaneie a rede do próprio compose (`172.28.0.0/24`), que tem hosts reais.

## Notas de produção

- Troque o certificado self-signed por um emitido por uma CA confiável (monte
  sobre `/certs`) e deixe `NTM_TLS_INSECURE=false` no agente.
- Use um `NTM_INSTALL_TOKEN` forte e rotacione-o; cada agente recebe um token
  próprio no enrollment.
- O ping da fase 1 (socket ICMP) precisa de `CAP_NET_RAW` (já concedido no
  compose e no unit systemd); as demais fases são TCP connect, sem privilégio.
- O download do agente é servido a partir de `platform/web/ntm_agent.py`, que é
  uma **cópia** de `agent/ntm_agent.py` (embutida no binário da plataforma).
  Ao alterar o agente, copie o arquivo: `cp agent/ntm_agent.py platform/web/`.
