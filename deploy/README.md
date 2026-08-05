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

2. Edite o bloco `CONFIG` no topo de [`deploy.py`](deploy.py):
   ```python
   HOST = "216.152.144.82"
   USER = "root"
   PASSWORD = "..."
   WM_PORT = 8085          # NÃO use 8080 (evolution)
   ```

3. Rode:
   ```
   python deploy.py
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
- Registra o processo no PM2 (`pm2 save`) e faz um health check autenticado.

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
