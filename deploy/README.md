# Deploy do whatsmiau em máquinas de licenciados

Sobe o whatsmiau **ao lado** do evolution-api numa máquina que já roda a stack
PM2 + Postgres + Redis (sem Docker), sem tocar no evolution nem no Zapeada.

## Como usar

1. Instale as dependências locais (uma vez):
   ```
   pip install paramiko
   ```
   e tenha o **Go** instalado (para compilar o binário Linux) — ou aponte
   `PREBUILT_BINARY` para um binário `linux/amd64` já pronto.

2. Configure o alvo — por variável de ambiente (preferido, não deixa senha em
   arquivo versionado):
   ```bash
   WM_HOST=216.152.144.82 WM_PASSWORD=... python deploy.py
   ```
   ou editando o bloco `CONFIG` no topo de [`deploy.py`](deploy.py):
   ```python
   HOST = "216.152.144.82"
   USER = "root"
   PASSWORD = "..."
   WM_PORT = 8085          # NÃO use 8080 (evolution)
   ```
   O ambiente vence o arquivo. As demais chaves do bloco (`WM_PORT`,
   `WM_REDIS_DB`, `WM_DIR`, `ZAPEADA_ENV`, `PREBUILT_BINARY`) aceitam o mesmo
   tratamento — útil para deployar em várias máquinas sem editar o script.

3. Para a segunda máquina em diante, reaproveite o binário já compilado:
   ```bash
   WM_HOST=177.70.11.251 WM_PASSWORD=... \
     PREBUILT_BINARY=deploy/whatsmiau-linux-amd64 python deploy.py
   ```

O script compila o binário estático (`CGO_ENABLED=0`, dialeto Postgres), envia
por SFTP, e executa o [`bootstrap.sh`](bootstrap.sh) no servidor. É **idempotente**.

## O que o bootstrap faz no servidor

- Cria um database + role Postgres dedicados (`whatsmiau`), reaproveitando o
  Postgres existente (sem SQLite/CGO).
- Isola o whatsmiau num **DB lógico próprio do Redis** compartilhado (`REDIS_DB`,
  default 5) — evita colisão com o BullMQ/cache do Zapeada.
- Lê as credenciais de Redis/Postgres do `.env` do Zapeada automaticamente.
- Usa a **mesma `EVOLUTION_API_KEY` do Zapeada** como `API_KEY` do whatsmiau (veja
  abaixo). Sem o `.env` do Zapeada, gera e persiste uma chave própria.
- Persiste a senha do DB (estável entre re-deploys).
- Preserva a configuração de proxy da máquina (`PROXY_POOL_FILE`,
  `PROXY_POOL_ROTATION`, `PROXY_POOL_COOLDOWN`, `PROXY_NO_MEDIA`): o `.env` é
  reescrito inteiro a cada deploy, então essas chaves são relidas do `.env`
  atual e reemitidas. Sem isso, um re-deploy tiraria as instâncias de trás do
  proxy sem avisar. Para mudar no deploy, exporte a variável antes do
  `bootstrap.sh` — ela vence o que está no arquivo.
- Registra o processo no PM2 (`pm2 save`) e faz um health check autenticado.

### Ligar o pool de proxies numa máquina

```bash
cp /caminho/proxies.json /home/whatsmiau/proxies.json   # formato igual ao da evolution
chmod 600 /home/whatsmiau/proxies.json
cat >> /home/whatsmiau/.env <<'EOF'
PROXY_POOL_FILE=/home/whatsmiau/proxies.json
PROXY_POOL_ROTATION=round_robin
PROXY_POOL_COOLDOWN=5m
EOF
pm2 restart whatsmiau --update-env
```

Confirme no log: `proxy pool loaded` com o `size` esperado e um
`proxy configured ... source=pool` por instância. Lembre que o pool é
**fail-closed**: com o arquivo ausente ou vazio, as instâncias não conectam.

#### Testar um proxy do pool: use um alvo IPv6

As saídas do VPS 3proxy são **IPv6**. Um teste contra um alvo só-IPv4 volta
`HTTP/1.0 502 Bad Gateway` e parece um pool quebrado — não é:

```bash
PX="http://evo:<senha>@186.194.48.234:30000"
curl -x "$PX" https://ipv6.icanhazip.com   # OK -> imprime o IPv6 de saída
curl -x "$PX" https://api.ipify.org        # 502 -> só-IPv4, não prova nada
```

O que interessa é o destino real: `g.whatsapp.net`, `web.whatsapp.com` e
`mmg.whatsapp.net` têm registro AAAA, então o túnel funciona. Teste o
`CONNECT`, não um GET — o `g.whatsapp.net` não responde a HTTP simples e
devolve `000` mesmo com o túnel de pé:

```bash
curl -sv -x "$PX" https://g.whatsapp.net 2>&1 | grep -i "Connection established"
```

### Por que a API key é a mesma do evolution

O `EvolutionAPIClient` do Zapeada usa **um único par url/chave** para os dois
providers — o whatsmiau é compatível com a API da Evolution. Se as chaves
diferissem, migrar uma empresa exigiria trocar a credencial junto com o provider,
e esquecer disso faz o Zapeada apresentar a chave do evolution ao whatsmiau: **401
em tudo**, a instância nunca é criada e o QR nunca aparece — sem erro claro na
tela, a conexão só fica em "tentando conexão...".

Com as chaves alinhadas, migrar uma empresa é trocar **provider + URL**, e a chave
continua valendo para os dois.

### Configurar o Zapeada

Uma vez por máquina, no `.env` (o webhook é o caminho de ingestão do whatsmiau):

```
WHATSMIAU_WEBHOOK_URL=http://127.0.0.1:8083/whatsmiau/webhook
WHATSMIAU_WEBHOOK_SECRET=<segredo forte>
# depois: pm2 restart api worker-heavy-1 worker-heavy-2 worker-light --update-env
```

Depois, por empresa, em **Configurações → Integrações → WhatsApp Web**: escolha o
provedor `whatsmiau` e aponte a URL para `http://127.0.0.1:8085/v1`. Para migrar a
máquina inteira de uma vez, use `WHATSAPP_PROVIDER=whatsmiau` +
`EVOLUTION_API_URL` no `.env` — empresas sem override herdam esse default.

> A migração do Zapeada é um passo **separado e manual** — o deploy só instala o
> whatsmiau, que fica ocioso até o Zapeada apontar para ele.
