#!/usr/bin/env bash
#
# bootstrap.sh — instala/atualiza o whatsmiau numa máquina de licenciado que já
# roda o evolution-api (stack PM2 + Postgres + Redis, sem Docker).
#
# É idempotente: pode rodar várias vezes. NÃO toca no evolution nem no Zapeada —
# apenas sobe o whatsmiau ao lado, num DB Postgres próprio e num DB lógico
# isolado do Redis compartilhado. É executado pelo deploy.py (via SSH), mas
# também pode ser rodado manualmente: `sudo bash bootstrap.sh`.
#
# Variáveis de ambiente (com defaults):
#   WM_DIR        diretório de instalação            (/home/whatsmiau)
#   WM_PORT       porta HTTP do whatsmiau            (8085)
#   WM_REDIS_DB   índice do DB lógico no Redis       (5)
#   ZAPEADA_ENV   .env de onde ler creds Redis/PG    (/home/deploy/backend/.env)
#
set -euo pipefail

WM_DIR=${WM_DIR:-/home/whatsmiau}
WM_PORT=${WM_PORT:-8085}
WM_REDIS_DB=${WM_REDIS_DB:-5}
ZAPEADA_ENV=${ZAPEADA_ENV:-/home/deploy/backend/.env}
WM_DB_NAME=whatsmiau
WM_DB_USER=whatsmiau

log() { echo "[whatsmiau-deploy] $*"; }
die() { echo "[whatsmiau-deploy][ERRO] $*" >&2; exit 1; }

command -v pm2 >/dev/null 2>&1 || die "pm2 não encontrado no PATH"
command -v sudo >/dev/null 2>&1 || die "sudo não encontrado"
[ -x "$WM_DIR/whatsmiau" ] || die "binário $WM_DIR/whatsmiau ausente/não-executável (o deploy.py deve enviá-lo antes)"

# --- 0. ffmpeg: obrigatório para áudio ---------------------------------------
# O envio de áudio transcodifica o .ogg antes de subir (PTT opus). Sem ffmpeg o
# whatsmiau responde 500 com "ffmpeg not found in path" e SÓ o áudio quebra —
# texto e imagem seguem funcionando, o que despista o diagnóstico. A imagem
# Docker já traz o ffmpeg; este deploy é binário puro, então instalamos aqui.
if ! command -v ffmpeg >/dev/null 2>&1; then
  log "ffmpeg ausente; instalando (necessário para envio de áudio)..."
  if command -v apt-get >/dev/null 2>&1; then
    DEBIAN_FRONTEND=noninteractive apt-get install -y ffmpeg >/dev/null 2>&1 \
      || log "AVISO: falha ao instalar ffmpeg — o envio de áudio vai falhar até instalar manualmente"
  else
    log "AVISO: apt-get não encontrado; instale o ffmpeg manualmente ou o áudio não funciona"
  fi
fi
command -v ffmpeg >/dev/null 2>&1 && log "ffmpeg: $(command -v ffmpeg)"

# --- 1. lê credenciais de Redis/Postgres do .env do Zapeada -------------------
getenv() { grep -E "^$1=" "$ZAPEADA_ENV" 2>/dev/null | head -1 | cut -d= -f2- | sed -e 's/^"//' -e 's/"$//' -e "s/^'//" -e "s/'$//"; }

if [ -f "$ZAPEADA_ENV" ]; then
  REDIS_HOST=$(getenv REDIS_HOST)
  REDIS_PORT=$(getenv REDIS_PORT)
  REDIS_PASSWORD=$(getenv REDIS_PASSWORD)
  PG_HOST=$(getenv DB_HOST)
  PG_PORT=$(getenv DB_PORT)
else
  log "AVISO: $ZAPEADA_ENV não encontrado; usando defaults locais"
fi
REDIS_HOST=${REDIS_HOST:-127.0.0.1}
REDIS_PORT=${REDIS_PORT:-6379}
PG_HOST=${PG_HOST:-127.0.0.1}
PG_PORT=${PG_PORT:-5432}

mkdir -p "$WM_DIR"

# --- 2. senha do DB e API key persistidas (estáveis entre re-deploys) ---------
persist_secret() { # $1=arquivo  -> gera hex se não existir, ecoa o valor
  local f="$1"
  if [ ! -s "$f" ]; then openssl rand -hex "${2:-16}" | tr -d '\n' > "$f"; chmod 600 "$f"; fi
  cat "$f"
}
WM_DB_PASS=$(persist_secret "$WM_DIR/.db_pass" 16)

# A API key do whatsmiau é a MESMA EVOLUTION_API_KEY do Zapeada, de propósito.
# O EvolutionAPIClient usa um único par url/chave para os dois providers, então
# chaves distintas obrigariam a trocar a credencial junto com o provider — e
# esquecer disso faz o Zapeada apresentar a chave do evolution ao whatsmiau, que
# responde 401 em tudo (instância nunca criada, QR nunca aparece). Com a chave
# alinhada, migrar uma empresa exige trocar só o provider e a URL.
# Sem o .env do Zapeada, cai no segredo próprio gerado/persistido de sempre.
ZAP_API_KEY=""
[ -f "$ZAPEADA_ENV" ] && ZAP_API_KEY=$(getenv EVOLUTION_API_KEY)
if [ -n "$ZAP_API_KEY" ]; then
  printf '%s' "$ZAP_API_KEY" > "$WM_DIR/.api_key"
  chmod 600 "$WM_DIR/.api_key"
  API_KEY="$ZAP_API_KEY"
  log "API key alinhada com a EVOLUTION_API_KEY do Zapeada"
else
  API_KEY=$(persist_secret "$WM_DIR/.api_key" 24)
  log "AVISO: EVOLUTION_API_KEY não encontrada em $ZAPEADA_ENV; usando API key própria"
fi

# --- 3. Postgres: cria role + database (idempotente, via peer auth) -----------
log "configurando Postgres (role/db '$WM_DB_NAME')..."
sudo -u postgres psql -v ON_ERROR_STOP=1 -q <<SQL
DO \$\$
BEGIN
  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = '$WM_DB_USER') THEN
    CREATE ROLE $WM_DB_USER LOGIN PASSWORD '$WM_DB_PASS';
  ELSE
    ALTER ROLE $WM_DB_USER LOGIN PASSWORD '$WM_DB_PASS';
  END IF;
END
\$\$;
SQL
if ! sudo -u postgres psql -tAc "SELECT 1 FROM pg_database WHERE datname='$WM_DB_NAME'" | grep -q 1; then
  sudo -u postgres createdb -O "$WM_DB_USER" "$WM_DB_NAME"
  log "database '$WM_DB_NAME' criado"
else
  log "database '$WM_DB_NAME' já existe"
fi

# --- 4. escreve o .env do whatsmiau ------------------------------------------
umask 077
cat > "$WM_DIR/.env" <<ENV
PORT=$WM_PORT
DEBUG_MODE=false
REDIS_URL=$REDIS_HOST:$REDIS_PORT
REDIS_PASSWORD=$REDIS_PASSWORD
REDIS_DB=$WM_REDIS_DB
API_KEY=$API_KEY
DIALECT_DB=postgres
DB_URL=postgres://$WM_DB_USER:$WM_DB_PASS@$PG_HOST:$PG_PORT/$WM_DB_NAME?sslmode=disable
GCS_ENABLED=false
ENV
chmod 600 "$WM_DIR/.env"
chmod +x "$WM_DIR/whatsmiau"

# --- 5. PM2: sobe/atualiza o processo (cwd = WM_DIR p/ o godotenv achar .env) --
log "(re)iniciando processo pm2 'whatsmiau'..."
if pm2 describe whatsmiau >/dev/null 2>&1; then
  pm2 restart whatsmiau --update-env
else
  # --interpreter none: executa o binário ELF diretamente
  pm2 start "$WM_DIR/whatsmiau" --name whatsmiau --cwd "$WM_DIR" --interpreter none
fi
pm2 save >/dev/null 2>&1 || true

# --- 6. health check (autenticado: exercita HTTP + Redis + Postgres) ----------
sleep 3
CODE=$(curl -s -m 5 -o /dev/null -w "%{http_code}" \
  -H "apikey: $API_KEY" \
  "http://127.0.0.1:$WM_PORT/v1/instance/fetchInstances" || echo "000")
if [ "$CODE" = "200" ]; then
  log "OK — whatsmiau saudável em http://127.0.0.1:$WM_PORT/v1 (HTTP $CODE)"
else
  log "AVISO — health retornou HTTP $CODE; últimas linhas do log:"
  pm2 logs whatsmiau --lines 30 --nostream || true
fi

# --- 7. resumo p/ configurar o Zapeada ---------------------------------------
echo ""
echo "==================== WHATSMIAU DEPLOY OK ===================="
echo "URL base (interna):  http://127.0.0.1:$WM_PORT/v1"
if [ -n "$ZAP_API_KEY" ]; then
echo "API key:             a MESMA EVOLUTION_API_KEY do Zapeada (alinhada de propósito)"
else
echo "API key:             $API_KEY  (própria — EVOLUTION_API_KEY não encontrada)"
fi
echo "Redis DB lógico:     $WM_REDIS_DB"
echo "Postgres db/role:    $WM_DB_NAME / $WM_DB_USER"
echo ""
BACKEND_PORT=$(getenv PORT); BACKEND_PORT=${BACKEND_PORT:-8083}
echo "PRÉ-REQUISITO: o backend do Zapeada nesta máquina precisa conter o código de"
echo "integração (rota /whatsmiau/webhook + provider flag). Sem isso, NÃO faça o flip."
echo ""
echo "Webhook (uma vez por máquina), no $ZAPEADA_ENV:"
echo "  WHATSMIAU_WEBHOOK_URL=http://127.0.0.1:$BACKEND_PORT/whatsmiau/webhook"
echo "  WHATSMIAU_WEBHOOK_SECRET=<gere um segredo forte>"
echo "  # depois: pm2 restart api worker-heavy-1 worker-heavy-2 worker-light --update-env"
echo ""
echo "Migrar UMA empresa (Configurações > Integrações > WhatsApp Web):"
echo "  Provedor   = whatsmiau"
echo "  URL da API = http://127.0.0.1:$WM_PORT/v1"
if [ -n "$ZAP_API_KEY" ]; then
echo "  Chave      = não precisa mexer (é a mesma dos dois providers)"
fi
echo ""
echo "Migrar a máquina INTEIRA (default global), no $ZAPEADA_ENV:"
echo "  WHATSAPP_PROVIDER=whatsmiau"
echo "  EVOLUTION_API_URL=http://127.0.0.1:$WM_PORT/v1"
echo "============================================================"
