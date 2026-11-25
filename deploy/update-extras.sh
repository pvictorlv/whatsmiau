#!/bin/bash
#
# update-extras.sh — atualiza o whatsmiau, o evolution-api e o paralela-voice desta máquina,
# se existirem, e garante o Google Chrome e as fontes que o Zapeada usa para gerar o PDF das propostas.
#
# Chamado pelo /home/Installer/cron.sh, que só o aciona quando o servidor está
# marcado como `waiting_update` no painel: marcar a máquina é a autorização para
# reiniciar o WhatsApp dos clientes, e este script não cria um segundo caminho
# que a contorne. Também pode ser rodado à mão: `bash update-extras.sh`.
#
#   DRY_RUN=1    só diz o que faria (baixa e confere o binário, não troca nada)
#   ONLY=miau    só o whatsmiau        ONLY=evolution   só o evolution-api
#   ONLY=voice   só o paralela-voice
#   ONLY=chrome  só o Chrome do PDF das propostas    ONLY=fonts   só as fontes do PDF
#
# Cada etapa registra o resultado e a seguinte roda. Uma etapa que falha desfaz a
# própria troca e deixa o serviço como estava. Sai com 2 só quando um serviço ficou
# FORA DO AR depois disso; qualquer outra falha sai com 0.

WM_DIR=${WM_DIR:-/home/whatsmiau}
EVO_DIR=${EVO_DIR:-/home/evolution-api}
VOICE_DIR=${VOICE_DIR:-/opt/paralela-voice}
VOICE_SERVICE=${VOICE_SERVICE:-paralela-voice}
INSTALLER_DIR=${INSTALLER_DIR:-/home/Installer}
PANEL_URL=${PANEL_URL:-https://system.paralela.net}
WM_REPO=${WM_REPO:-pvictorlv/whatsmiau}
RELEASE_URL="https://github.com/${WM_REPO}/releases/latest/download"
ASSET=whatsmiau-linux-amd64
LOG=${LOG:-/home/Installer/update-extras.log}
LOCK_FILE=${LOCK_FILE:-/var/lock/update-extras.lock}
CHROME_BIN=${CHROME_BIN:-/opt/google/chrome/chrome}
CHROME_URL=${CHROME_URL:-https://dl.google.com/linux/direct/google-chrome-stable_current_amd64.deb}
FONT_DIR=${FONT_DIR:-/usr/local/share/fonts/proposal}
FONT_BASE=${FONT_BASE:-https://github.com/google/fonts/raw/main/ofl}
KEEP_BACKUPS=3
DRY_RUN=${DRY_RUN:-0}
ONLY=${ONLY:-all}

# O tsc do evolution e o do Zapeada estouram o heap padrão do node nestas máquinas.
export NODE_OPTIONS=${NODE_OPTIONS:---max-old-space-size=4096}

DOWN=""   # serviços que ficaram fora do ar

log() { echo "$(date '+%F %T') [update-extras] $*" | tee -a "$LOG"; }

# Duas execuções ao mesmo tempo (cron + mão) reiniciariam o mesmo serviço duas vezes.
exec 9>"$LOCK_FILE"
if ! flock -n 9; then
  log "outra execução em andamento; saindo"
  exit 0
fi

env_value() { # $1=arquivo $2=chave -> valor sem aspas, ou vazio
  grep -E "^$2=" "$1" 2>/dev/null | head -1 | cut -d= -f2- | sed -e 's/^"//' -e 's/"$//' -e "s/^'//" -e "s/'$//"
}

prune_backups() { # $1=prefixo do arquivo -> mantém só os KEEP_BACKUPS mais novos
  ls -1t "$1".bak-* 2>/dev/null | tail -n +$((KEEP_BACKUPS + 1)) | xargs -r rm -rf
}

# --------------------------------------------------------------------------- #
# whatsmiau
# --------------------------------------------------------------------------- #
wm_healthy() { # $1=porta $2=api key [$3=tentativas] -> 0 se responder 200 autenticado
  local i code
  for i in $(seq 1 "${3:-8}"); do
    [ "$i" -gt 1 ] && sleep 4
    code=$(curl -s -m 5 -o /dev/null -w '%{http_code}' -H "apikey: $2" \
      "http://127.0.0.1:$1/v1/instance/fetchInstances")
    [ "$code" = 200 ] && return 0
  done
  return 1
}

wm_pid() { # pid do processo do pm2, ou 0
  local pid
  pid=$(pm2 pid whatsmiau 2>/dev/null | tail -1 | tr -dc 0-9)
  echo "${pid:-0}"
}

wm_recover() { # $1=porta $2=api key -> reinicia até voltar; registra em DOWN se não voltar
  local attempt
  for attempt in 1 2 3; do
    # O bootstrap apaga o processo antes de recriá-lo: pode não haver o que reiniciar.
    pm2 restart whatsmiau --exp-backoff-restart-delay=1000 >>"$LOG" 2>&1 \
      || pm2 start "$WM_DIR/whatsmiau" --name whatsmiau --cwd "$WM_DIR" --interpreter none \
           --max-memory-restart "${WM_MAX_MEMORY:-6G}" --exp-backoff-restart-delay=1000 >>"$LOG" 2>&1
    # 60s: é o quanto o whatsmiau espera por um banco que recusa conexão.
    if wm_healthy "$1" "$2" 16; then
      pm2 save >/dev/null 2>&1
      log "whatsmiau: no ar depois da tentativa $attempt"
      return 0
    fi
    log "whatsmiau: tentativa $attempt de religar não respondeu"
    sleep 10
  done
  log "whatsmiau: FORA DO AR depois de 3 tentativas; precisa de intervenção"
  DOWN="$DOWN whatsmiau"
  return 1
}

update_whatsmiau() {
  if [ ! -x "$WM_DIR/whatsmiau" ]; then
    log "whatsmiau: não instalado em $WM_DIR; nada a fazer"
    return 0
  fi

  local tmp want have port redis_db api_key
  port=$(env_value "$WM_DIR/.env" PORT);         port=${port:-8085}
  redis_db=$(env_value "$WM_DIR/.env" REDIS_DB); redis_db=${redis_db:-5}
  api_key=$(env_value "$WM_DIR/.env" API_KEY);   api_key=${api_key:-$(cat "$WM_DIR/.api_key" 2>/dev/null)}

  tmp=$(mktemp -d) || { log "whatsmiau: sem diretório temporário"; return 1; }
  # shellcheck disable=SC2064
  trap "rm -rf '$tmp'" RETURN

  if ! curl -fsSL --retry 3 -m 600 -o "$tmp/$ASSET" "$RELEASE_URL/$ASSET" \
     || ! curl -fsSL --retry 3 -m 60 -o "$tmp/$ASSET.sha256" "$RELEASE_URL/$ASSET.sha256" \
     || ! curl -fsSL --retry 3 -m 60 -o "$tmp/bootstrap.sh" "$RELEASE_URL/bootstrap.sh"; then
    log "whatsmiau: falha ao baixar o release de $WM_REPO; nada foi alterado"
    return 1
  fi

  want=$(awk '{print $1}' "$tmp/$ASSET.sha256")
  have=$(sha256sum "$tmp/$ASSET" | awk '{print $1}')
  if [ -z "$want" ] || [ "$want" != "$have" ]; then
    log "whatsmiau: sha256 do download não confere (esperado=$want obtido=$have); nada foi alterado"
    return 1
  fi
  # Uma página de erro do GitHub com status 200 também passaria por "arquivo baixado".
  if [ "$(head -c4 "$tmp/$ASSET" | od -An -tx1 | tr -d ' \n')" != "7f454c46" ] \
     || [ "$(stat -c %s "$tmp/$ASSET")" -lt 10000000 ]; then
    log "whatsmiau: o download não é um binário ELF plausível; nada foi alterado"
    return 1
  fi

  have=$(sha256sum "$WM_DIR/whatsmiau" | awk '{print $1}')
  if [ "$have" = "$want" ]; then
    log "whatsmiau: já está na versão do release (${want:0:12})"
    if [ "$DRY_RUN" != 1 ] && ! wm_healthy "$port" "$api_key"; then
      log "whatsmiau: não responde na porta $port; religando"
      wm_recover "$port" "$api_key" || return 1
    fi
    return 0
  fi
  if [ "$DRY_RUN" = 1 ]; then
    log "whatsmiau: [dry-run] trocaria ${have:0:12} por ${want:0:12} e rodaria o bootstrap"
    return 0
  fi

  # O bootstrap reescreve o .env inteiro a partir destas variáveis: sem repassar a
  # porta e o DB do Redis que a máquina já usa, uma instalação fora do padrão
  # voltaria aos defaults sem aviso.
  local stamp pid_before
  stamp=$(date +%Y%m%d-%H%M%S)
  pid_before=$(wm_pid)

  cp -p "$WM_DIR/whatsmiau" "$WM_DIR/whatsmiau.bak-$stamp"
  cp -p "$WM_DIR/bootstrap.sh" "$WM_DIR/bootstrap.sh.bak-$stamp" 2>/dev/null
  cp -p "$WM_DIR/.env" "$WM_DIR/.env.bak-$stamp" 2>/dev/null

  # Troca atômica: o mv evita o "text file busy" de sobrescrever o binário em uso.
  install -m 755 "$tmp/$ASSET" "$WM_DIR/whatsmiau.new" && mv -f "$WM_DIR/whatsmiau.new" "$WM_DIR/whatsmiau"
  install -m 644 "$tmp/bootstrap.sh" "$WM_DIR/bootstrap.sh"

  log "whatsmiau: ${have:0:12} -> ${want:0:12}; rodando o bootstrap (porta $port, redis db $redis_db)"
  WM_DIR="$WM_DIR" WM_PORT="$port" WM_REDIS_DB="$redis_db" bash "$WM_DIR/bootstrap.sh" >>"$LOG" 2>&1
  local rc=$?

  # O health do bootstrap só avisa; aqui um 200 autenticado decide se a troca fica.
  # O bootstrap pode ter trocado a chave pela do Zapeada.
  local new_key
  new_key=$(cat "$WM_DIR/.api_key" 2>/dev/null)
  if [ $rc -eq 0 ] && wm_healthy "$port" "${new_key:-$api_key}" 16; then
    log "whatsmiau: atualizado e saudável (HTTP 200)"
    prune_backups "$WM_DIR/whatsmiau"; prune_backups "$WM_DIR/bootstrap.sh"; prune_backups "$WM_DIR/.env"
    return 0
  fi

  log "whatsmiau: FALHOU (bootstrap rc=$rc); voltando ao binário anterior"
  mv -f "$WM_DIR/whatsmiau.bak-$stamp" "$WM_DIR/whatsmiau"
  [ -f "$WM_DIR/bootstrap.sh.bak-$stamp" ] && cp -p "$WM_DIR/bootstrap.sh.bak-$stamp" "$WM_DIR/bootstrap.sh"
  [ -f "$WM_DIR/.env.bak-$stamp" ] && cp -p "$WM_DIR/.env.bak-$stamp" "$WM_DIR/.env"

  # Quando o bootstrap falha antes do pm2, o processo antigo nunca parou: reiniciá-lo
  # aqui derrubava um serviço bom justo enquanto o banco recusava conexões (#1637).
  if [ "$pid_before" != 0 ] && [ "$(wm_pid)" = "$pid_before" ] && wm_healthy "$port" "$api_key" 3; then
    log "whatsmiau: o processo anterior segue no ar; sem restart"
    return 1
  fi
  wm_recover "$port" "$api_key"
  return 1
}

# --------------------------------------------------------------------------- #
# evolution-api
# --------------------------------------------------------------------------- #
evolution_pm2_name() { # processo do pm2 cujo cwd é o evolution; cai no nome de costume
  local name
  name=$(pm2 jlist 2>/dev/null | jq -r --arg d "$EVO_DIR" '[.[] | select(.pm2_env.pm_cwd == $d)][0].name // empty' 2>/dev/null)
  echo "${name:-ApiEvolution}"
}

evolution_healthy() { # $1=porta -> espera até ~60 s por um HTTP 200 na raiz
  local i code
  for i in $(seq 1 12); do
    sleep 5
    code=$(curl -s -m 5 -o /dev/null -w '%{http_code}' "http://127.0.0.1:$1/" || echo 000)
    [ "$code" = 200 ] && return 0
  done
  return 1
}

evolution_rebuild() { # $1=mensagem de contexto -> instala, gera prisma, migra e compila
  (cd "$EVO_DIR" || exit 1
   { npm install || npm install --legacy-peer-deps; } \
     && npm run db:generate \
     && npm run db:deploy \
     && npm run build) >>"$LOG" 2>&1
}

update_evolution() {
  if [ ! -d "$EVO_DIR/.git" ]; then
    log "evolution: não instalado em $EVO_DIR; nada a fazer"
    return 0
  fi

  local branch old new name port
  branch=$(git -C "$EVO_DIR" rev-parse --abbrev-ref HEAD)
  if [ -z "$branch" ] || [ "$branch" = HEAD ]; then
    log "evolution: HEAD solto (sem branch); não atualizo"
    return 0
  fi

  # Segue a branch em que a máquina está (baileys-stable no demo), nunca a main:
  # as duas linhas divergem em dezenas de commits.
  if ! timeout 120 git -C "$EVO_DIR" fetch -q origin "$branch" 2>>"$LOG"; then
    log "evolution: git fetch origin $branch falhou; nada foi alterado"
    return 1
  fi
  old=$(git -C "$EVO_DIR" rev-parse HEAD)
  new=$(git -C "$EVO_DIR" rev-parse "origin/$branch" 2>/dev/null)
  if [ -z "$new" ] || [ "$old" = "$new" ]; then
    log "evolution: já está em origin/$branch (${old:0:8})"
    return 0
  fi
  if ! git -C "$EVO_DIR" merge-base --is-ancestor "$old" "$new"; then
    log "evolution: ${old:0:8} tem commit local fora de origin/$branch; não faço fast-forward por cima"
    return 1
  fi
  if [ "$DRY_RUN" = 1 ]; then
    log "evolution: [dry-run] avançaria $branch ${old:0:8} -> ${new:0:8} ($(git -C "$EVO_DIR" rev-list --count "$old..$new") commits)"
    git -C "$EVO_DIR" status --porcelain --untracked-files=no | sed 's/^/                    local: /' | tee -a "$LOG"
    return 0
  fi

  local stash=""
  if [ -n "$(git -C "$EVO_DIR" status --porcelain --untracked-files=no)" ]; then
    # Alterações locais em arquivo rastreado (hoje: o package.json com o hash do
    # Baileys) impedem o pull. Vão para o stash, não para o lixo: ficam em
    # `git stash list` para quem precisar delas de volta.
    log "evolution: alterações locais guardadas no stash:"
    git -C "$EVO_DIR" status --porcelain --untracked-files=no | tee -a "$LOG"
    git -C "$EVO_DIR" stash push -q -m "update-extras $(date +%F-%T)" && stash=1
  fi

  name=$(evolution_pm2_name)
  port=$(env_value "$EVO_DIR/.env" SERVER_PORT); port=${port:-8080}

  rm -rf "$EVO_DIR/dist.prev" && cp -a "$EVO_DIR/dist" "$EVO_DIR/dist.prev" 2>/dev/null
  log "evolution: $branch ${old:0:8} -> ${new:0:8}"

  if git -C "$EVO_DIR" merge --ff-only -q "origin/$branch" 2>>"$LOG" \
     && evolution_rebuild \
     && pm2 restart "$name" --update-env >>"$LOG" 2>&1 \
     && evolution_healthy "$port"; then
    log "evolution: atualizado e respondendo na porta $port"
    rm -rf "$EVO_DIR/dist.prev"
    return 0
  fi

  log "evolution: FALHOU; voltando para ${old:0:8}"
  git -C "$EVO_DIR" reset -q --hard "$old"
  [ -n "$stash" ] && git -C "$EVO_DIR" stash pop -q 2>>"$LOG"
  if [ -d "$EVO_DIR/dist.prev" ]; then
    rm -rf "$EVO_DIR/dist" && mv "$EVO_DIR/dist.prev" "$EVO_DIR/dist"
  fi
  (cd "$EVO_DIR" && { npm install || npm install --legacy-peer-deps; }) >>"$LOG" 2>&1
  pm2 restart "$name" --update-env >>"$LOG" 2>&1
  if ! evolution_healthy "$port"; then
    log "evolution: FORA DO AR depois do rollback; precisa de intervenção"
    DOWN="$DOWN evolution"
  fi
  return 1
}

# --------------------------------------------------------------------------- #
# paralela-voice
# --------------------------------------------------------------------------- #
# O repositório do voice é privado e estas máquinas são dos licenciados: nenhuma tem
# token do GitHub. O painel guarda a release promovida e a entrega com a mesma key.txt
# do pacote do Zapeada.
voice_unit_value() { # $1=opção do ExecStart (-addr, -db) -> valor, ou vazio
  systemctl cat "$VOICE_SERVICE" 2>/dev/null | grep -E '^ExecStart=' | tail -1 \
    | grep -oE -- "$1[ =][^ ]+" | head -1 | sed -E "s/^$1[ =]//"
}

voice_healthy() { # $1=porta [$2=tentativas] -> 0 se o serviço atende
  local i code
  for i in $(seq 1 "${2:-15}"); do
    [ "$i" -gt 1 ] && sleep 2
    code=$(curl -s -m 5 -o /dev/null -w '%{http_code}' "http://127.0.0.1:$1/healthz")
    # 401/404: binário antigo, sem a rota, mas de pé. Importa na volta atrás.
    case "$code" in 200|401|404) return 0;; esac
  done
  return 1
}

voice_backup_db() { # $1=arquivo do banco $2=carimbo; com o serviço parado, para copiar um banco íntegro
  local dir="$VOICE_DIR/backups" suffix
  [ -f "$1" ] || return 0
  mkdir -p "$dir" || return 1
  for suffix in "" -wal -shm; do
    if [ -f "$1$suffix" ]; then
      cp -p "$1$suffix" "$dir/voice.db$suffix.$2" || return 1
    fi
  done
  # Só as cópias feitas aqui (AAAAMMDD-HHMMSS): as manuais, sem o hífen, ficam.
  ls -1 "$dir"/voice.db.[0-9]*-[0-9]* 2>/dev/null | sort -r | tail -n +$((KEEP_BACKUPS + 1)) | while read -r old; do
    old=${old##*/voice.db.}
    rm -f "$dir/voice.db.$old" "$dir/voice.db-wal.$old" "$dir/voice.db-shm.$old"
  done
}

update_voice() {
  local bin="$VOICE_DIR/paralela-voice"
  if [ ! -x "$bin" ]; then
    log "voice: não instalado em $VOICE_DIR; nada a fazer"
    return 0
  fi

  local key meta path want have version port db tmp stamp
  key=$(cat "$INSTALLER_DIR/key.txt" 2>/dev/null)
  if [ -z "$key" ]; then
    log "voice: sem key.txt para baixar do painel; nada foi alterado"
    return 1
  fi
  if ! meta=$(curl -fsSL --retry 3 -m 60 -H "Authorization: Bearer $key" "$PANEL_URL/api/downloads/last?branch=voice"); then
    log "voice: o painel não informou a versão; nada foi alterado"
    return 1
  fi
  path=$(jq -r '.data.path // empty' <<<"$meta" 2>/dev/null)
  want=$(jq -r '.data.checksum // empty' <<<"$meta" 2>/dev/null)
  version=$(jq -r '.data.version // empty' <<<"$meta" 2>/dev/null)
  if [ -z "$path" ] || ! [[ "$want" =~ ^[0-9a-f]{64}$ ]]; then
    log "voice: resposta inesperada do painel; nada foi alterado"
    return 1
  fi

  port=$(voice_unit_value -addr); port=${port##*:}; port=${port:-8091}
  db=$(voice_unit_value -db); db=${db:-$VOICE_DIR/data/voice.db}

  have=$(sha256sum "$bin" | awk '{print $1}')
  if [ "$have" = "$want" ]; then
    log "voice: já está em $version (${want:0:12})"
    if [ "$DRY_RUN" != 1 ] && ! voice_healthy "$port" 3; then
      log "voice: não responde na porta $port; religando"
      systemctl restart "$VOICE_SERVICE" >>"$LOG" 2>&1
      if ! voice_healthy "$port"; then
        log "voice: FORA DO AR; precisa de intervenção"
        DOWN="$DOWN voice"
        return 1
      fi
    fi
    return 0
  fi
  if [ "$DRY_RUN" = 1 ]; then
    log "voice: [dry-run] trocaria ${have:0:12} por ${want:0:12} ($version)"
    return 0
  fi

  tmp=$(mktemp -d) || { log "voice: sem diretório temporário"; return 1; }
  # shellcheck disable=SC2064
  trap "rm -rf '$tmp'" RETURN

  if ! curl -fsSL --retry 3 -m 600 -H "Authorization: Bearer $key" -o "$tmp/voice" "$PANEL_URL/api/download?file_name=$path"; then
    log "voice: falha ao baixar $version; nada foi alterado"
    return 1
  fi
  have=$(sha256sum "$tmp/voice" | awk '{print $1}')
  if [ "$want" != "$have" ]; then
    log "voice: sha256 do download não confere (esperado=$want obtido=$have); nada foi alterado"
    return 1
  fi
  if [ "$(head -c4 "$tmp/voice" | od -An -tx1 | tr -d ' \n')" != "7f454c46" ] \
     || [ "$(stat -c %s "$tmp/voice")" -lt 10000000 ]; then
    log "voice: o download não é um binário ELF plausível; nada foi alterado"
    return 1
  fi

  stamp=$(date +%Y%m%d-%H%M%S)
  if ! cp -p "$bin" "$bin.bak-$stamp" || ! install -m 755 "$tmp/voice" "$bin.new"; then
    rm -f "$bin.new"
    log "voice: não consegui preparar a troca (disco?); nada foi alterado"
    return 1
  fi

  log "voice: trocando para $version (porta $port)"
  systemctl stop "$VOICE_SERVICE" >>"$LOG" 2>&1
  # As migrations rodam sozinhas na partida: sem a cópia do banco não há como voltar.
  if voice_backup_db "$db" "$stamp"; then
    mv -f "$bin.new" "$bin"
    systemctl start "$VOICE_SERVICE" >>"$LOG" 2>&1
    if voice_healthy "$port"; then
      log "voice: atualizado e respondendo na porta $port"
      prune_backups "$bin"
      return 0
    fi
    log "voice: FALHOU; voltando ao binário anterior (cópia do banco em $VOICE_DIR/backups/voice.db.$stamp)"
    systemctl stop "$VOICE_SERVICE" >>"$LOG" 2>&1
    mv -f "$bin.bak-$stamp" "$bin"
  else
    log "voice: não consegui copiar o banco; a versão anterior foi mantida"
    rm -f "$bin.new" "$bin.bak-$stamp"
  fi

  systemctl start "$VOICE_SERVICE" >>"$LOG" 2>&1
  if ! voice_healthy "$port"; then
    log "voice: FORA DO AR depois da volta; precisa de intervenção"
    DOWN="$DOWN voice"
  fi
  return 1
}

# --------------------------------------------------------------------------- #
# Google Chrome (PDF das propostas)
# --------------------------------------------------------------------------- #
# O backend gera o PDF com playwright-core e procura primeiro o Chrome instalado
# (`channel: "chrome"`). Sem ele, a proposta falha com "o mecanismo de geração de
# PDF não está disponível". Instalar não reinicia nada: o backend procura o
# navegador a cada tentativa.
ensure_chrome() {
  if [ ! -d "${BACKEND_DIR:-/home/deploy/backend}" ]; then
    log "chrome: sem backend do Zapeada nesta máquina; nada a fazer"
    return 0
  fi
  if [ -x "$CHROME_BIN" ]; then
    log "chrome: já instalado ($("$CHROME_BIN" --version 2>/dev/null | tr -d '
'))"
    return 0
  fi
  if [ "$(dpkg --print-architecture 2>/dev/null)" != amd64 ]; then
    log "chrome: o .deb do Google é só amd64 e esta máquina é $(dpkg --print-architecture 2>/dev/null); instale um Chromium à mão"
    return 1
  fi
  if [ "$DRY_RUN" = 1 ]; then
    log "chrome: [dry-run] instalaria o google-chrome-stable de $CHROME_URL"
    return 0
  fi

  local tmp
  tmp=$(mktemp -d) || { log "chrome: sem diretório temporário"; return 1; }
  # shellcheck disable=SC2064
  trap "rm -rf '$tmp'" RETURN

  if ! curl -fsSL --retry 3 -m 600 -o "$tmp/chrome.deb" "$CHROME_URL"; then
    log "chrome: falha ao baixar $CHROME_URL; nada foi alterado"
    return 1
  fi
  # Uma página de erro com status 200 também passaria por "arquivo baixado".
  if ! dpkg-deb --field "$tmp/chrome.deb" Package 2>/dev/null | grep -qx google-chrome-stable; then
    log "chrome: o download não é o pacote google-chrome-stable; nada foi alterado"
    return 1
  fi
  if ! DEBIAN_FRONTEND=noninteractive apt-get install -y -qq -o DPkg::Lock::Timeout=120 "$tmp/chrome.deb" >>"$LOG" 2>&1; then
    log "chrome: apt-get install FALHOU (detalhes acima); nada mais foi alterado"
    return 1
  fi
  log "chrome: instalado ($("$CHROME_BIN" --version 2>/dev/null | tr -d '
'))"
}

# --------------------------------------------------------------------------- #
# Fontes do PDF das propostas
# --------------------------------------------------------------------------- #
# Os modelos citam Poppins, Inter e Georgia, e o servidor não traz nenhuma: sem elas o Chrome cai no DejaVu e
# a proposta sai com cara de documento de 2005. Poppins, Inter e Lora são OFL (google/fonts). Georgia não é livre:
# o fontconfig a aponta para a Lora. Novas versões do backend também levam as fontes dentro do documento; isto
# cobre o backend antigo e os modelos editados à mão.
FONT_FILES=(
  "Poppins-Regular.ttf|poppins/Poppins-Regular.ttf"
  "Poppins-Medium.ttf|poppins/Poppins-Medium.ttf"
  "Poppins-SemiBold.ttf|poppins/Poppins-SemiBold.ttf"
  "Poppins-Bold.ttf|poppins/Poppins-Bold.ttf"
  "Poppins-ExtraBold.ttf|poppins/Poppins-ExtraBold.ttf"
  "Inter[opsz,wght].ttf|inter/Inter%5Bopsz%2Cwght%5D.ttf"
  "Lora[wght].ttf|lora/Lora%5Bwght%5D.ttf"
)
GEORGIA_ALIAS=/etc/fonts/conf.d/61-proposal-georgia.conf

ensure_fonts() {
  if [ ! -d "${BACKEND_DIR:-/home/deploy/backend}" ]; then
    log "fontes: sem backend do Zapeada nesta máquina; nada a fazer"
    return 0
  fi
  if ! command -v fc-cache >/dev/null 2>&1; then
    log "fontes: fontconfig (fc-cache) não instalado; nada foi alterado"
    return 1
  fi

  local missing=() entry
  for entry in "${FONT_FILES[@]}"; do
    [ -s "$FONT_DIR/${entry%%|*}" ] || missing+=("$entry")
  done
  if [ ${#missing[@]} -eq 0 ] && [ -f "$GEORGIA_ALIAS" ]; then
    log "fontes: Poppins, Inter e Lora já instaladas"
    return 0
  fi
  if [ "$DRY_RUN" = 1 ]; then
    log "fontes: [dry-run] instalaria ${#missing[@]} arquivo(s) em $FONT_DIR e o alias Georgia -> Lora"
    return 0
  fi

  local tmp file url
  tmp=$(mktemp -d) || { log "fontes: sem diretório temporário"; return 1; }
  # shellcheck disable=SC2064
  trap "rm -rf '$tmp'" RETURN
  mkdir -p "$FONT_DIR" || { log "fontes: não consegui criar $FONT_DIR"; return 1; }

  for entry in "${missing[@]}"; do
    file=${entry%%|*}; url="$FONT_BASE/${entry#*|}"
    if ! curl -fsSL --retry 3 -m 120 -o "$tmp/font" "$url"; then
      log "fontes: falha ao baixar $url; as demais seguem"
      continue
    fi
    # Uma página de erro com status 200 também passaria por "arquivo baixado".
    if [ "$(head -c4 "$tmp/font" | od -An -tx1 | tr -d ' 
')" != "00010000" ] || [ "$(stat -c %s "$tmp/font")" -lt 20000 ]; then
      log "fontes: $file baixado não é um TTF plausível; ignorado"
      continue
    fi
    install -m 644 "$tmp/font" "$FONT_DIR/$file"
  done

  if [ ! -f "$GEORGIA_ALIAS" ]; then
    cat >"$GEORGIA_ALIAS" <<'XML'
<?xml version="1.0"?>
<!DOCTYPE fontconfig SYSTEM "fonts.dtd">
<fontconfig>
  <alias binding="same"><family>Georgia</family><prefer><family>Lora</family></prefer></alias>
</fontconfig>
XML
  fi
  fc-cache -f "$FONT_DIR" >>"$LOG" 2>&1

  local fam miss=""
  for fam in Poppins Inter Lora; do
    fc-list : family | grep -qx "$fam" || miss="$miss $fam"
  done
  if [ -n "$miss" ]; then
    log "fontes: instalação incompleta, faltam:$miss"
    return 1
  fi
  log "fontes: Poppins, Inter e Lora instaladas; Georgia aponta para a Lora"
}

[ "$DRY_RUN" = 1 ] && log "modo dry-run: nada será trocado"
case "$ONLY" in all|miau)      update_whatsmiau || log "whatsmiau: etapa terminou com erro";; esac
case "$ONLY" in all|evolution) update_evolution || log "evolution: etapa terminou com erro";; esac
case "$ONLY" in all|voice)     update_voice     || log "voice: etapa terminou com erro";; esac
case "$ONLY" in all|chrome)     ensure_chrome    || log "chrome: etapa terminou com erro";; esac
case "$ONLY" in all|fonts)      ensure_fonts     || log "fontes: etapa terminou com erro";; esac
if [ -n "$DOWN" ]; then
  log "serviços fora do ar:$DOWN"
  exit 2
fi
exit 0
