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
- Gera e **persiste** `API_KEY` e a senha do DB (estáveis entre re-deploys).
- Registra o processo no PM2 (`pm2 save`) e faz um health check autenticado.

Ao final imprime a URL interna, a API key e os valores de `.env` para plugar no
Zapeada quando você decidir migrar aquele licenciado:

```
WHATSAPP_PROVIDER=whatsmiau
EVOLUTION_API_URL=http://127.0.0.1:8085/v1
EVOLUTION_API_KEY=<api key impressa>
WHATSMIAU_WEBHOOK_URL=<url pública do backend>/whatsmiau/webhook
WHATSMIAU_WEBHOOK_SECRET=<segredo forte>
# depois: pm2 restart api worker-heavy-1 worker-heavy-2 worker-light --update-env
```

> A migração do Zapeada é um passo **separado e manual** — o deploy só instala o
> whatsmiau, que fica ocioso até o Zapeada apontar para ele.
