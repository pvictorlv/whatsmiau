#!/usr/bin/env python3
"""
deploy.py — deploy do whatsmiau numa máquina de licenciado (PM2 + Postgres +
Redis, sem Docker), ao lado do evolution-api existente.

USO:
  1. Preencha o bloco CONFIG abaixo (HOST / USER / PASSWORD).
  2. Rode:  python deploy.py
  3. O script compila o binário Linux localmente, envia binário + bootstrap.sh
     por SSH e executa o bootstrap no servidor. É idempotente (pode repetir).

Requisitos locais:
  - Python 3 + paramiko   ->  pip install paramiko
  - Go (para compilar)    ->  ou aponte PREBUILT_BINARY p/ um binário linux/amd64 já pronto

Nada do evolution/Zapeada é alterado: o whatsmiau sobe isolado. Ao final, o
script imprime a URL/API key para você plugar no Zapeada quando quiser migrar.
"""
import os
import sys
import subprocess
from pathlib import Path

# ============================ CONFIG — edite aqui ============================
HOST = "SEU_IP_AQUI"          # ex: 216.152.144.82
USER = "root"
PASSWORD = "SUA_SENHA_AQUI"
SSH_PORT = 22

WM_PORT = 8085                # porta HTTP do whatsmiau (NÃO use 8080 = evolution)
WM_REDIS_DB = 5               # DB lógico isolado no Redis compartilhado
WM_DIR = "/home/whatsmiau"    # diretório de instalação no servidor
ZAPEADA_ENV = "/home/deploy/backend/.env"  # de onde ler creds Redis/Postgres

PREBUILT_BINARY = ""          # opcional: caminho de um binário linux/amd64 pronto
                              # (se vazio, o script compila do repositório com Go)
# ===========================================================================

REPO_DIR = Path(__file__).resolve().parent.parent   # raiz do repo whatsmiau
BOOTSTRAP = Path(__file__).resolve().parent / "bootstrap.sh"


def find_go() -> str:
    import shutil
    for cand in ("go", os.path.expanduser(r"~\go-sdk\go\bin\go.exe"), r"C:\Go\bin\go.exe"):
        p = shutil.which(cand) if os.path.sep not in cand else (cand if os.path.exists(cand) else None)
        if p:
            return p
    raise SystemExit("Go não encontrado. Instale o Go ou defina PREBUILT_BINARY.")


def build_binary() -> Path:
    if PREBUILT_BINARY:
        p = Path(PREBUILT_BINARY)
        if not p.exists():
            raise SystemExit(f"PREBUILT_BINARY não existe: {p}")
        print(f"[deploy] usando binário pré-compilado: {p}")
        return p

    go = find_go()
    out = REPO_DIR / "deploy" / "whatsmiau-linux-amd64"
    env = dict(os.environ, GOOS="linux", GOARCH="amd64", CGO_ENABLED="0")
    print(f"[deploy] compilando binário linux/amd64 com {go} ...")
    subprocess.run(
        [go, "build", "-ldflags=-s -w", "-o", str(out), "."],
        cwd=str(REPO_DIR), env=env, check=True,
    )
    mb = out.stat().st_size / 1024 / 1024
    print(f"[deploy] binário pronto: {out} ({mb:.1f} MB)")
    return out


def main():
    if HOST == "SEU_IP_AQUI" or PASSWORD == "SUA_SENHA_AQUI":
        raise SystemExit("Edite o bloco CONFIG (HOST/USER/PASSWORD) antes de rodar.")
    if not BOOTSTRAP.exists():
        raise SystemExit(f"bootstrap.sh não encontrado em {BOOTSTRAP}")

    try:
        import paramiko
    except ImportError:
        raise SystemExit("paramiko ausente. Rode: pip install paramiko")

    binary = build_binary()

    print(f"[deploy] conectando em {USER}@{HOST}:{SSH_PORT} ...")
    cli = paramiko.SSHClient()
    cli.set_missing_host_key_policy(paramiko.AutoAddPolicy())
    cli.connect(HOST, port=SSH_PORT, username=USER, password=PASSWORD,
                timeout=25, look_for_keys=False, allow_agent=False)

    # 1. envia binário + bootstrap via SFTP
    sftp = cli.open_sftp()
    cli.exec_command(f"mkdir -p {WM_DIR}")[1].channel.recv_exit_status()
    print(f"[deploy] enviando binário -> {WM_DIR}/whatsmiau ...")
    sftp.put(str(binary), f"{WM_DIR}/whatsmiau.new")
    sftp.put(str(BOOTSTRAP), f"{WM_DIR}/bootstrap.sh")
    sftp.close()
    # troca atômica do binário (evita "text file busy" com o processo rodando)
    _run(cli, f"mv -f {WM_DIR}/whatsmiau.new {WM_DIR}/whatsmiau && chmod +x {WM_DIR}/whatsmiau")

    # 2. executa o bootstrap no servidor, transmitindo a saída
    envvars = (
        f"WM_DIR={sh(WM_DIR)} WM_PORT={WM_PORT} WM_REDIS_DB={WM_REDIS_DB} "
        f"ZAPEADA_ENV={sh(ZAPEADA_ENV)}"
    )
    cmd = f"sudo {envvars} bash {WM_DIR}/bootstrap.sh"
    print(f"[deploy] executando bootstrap no servidor...\n")
    rc = _stream(cli, cmd)
    cli.close()
    if rc != 0:
        raise SystemExit(f"[deploy] bootstrap terminou com código {rc}")
    print("\n[deploy] concluído.")


def sh(v: str) -> str:
    return "'" + v.replace("'", "'\\''") + "'"


def _run(cli, cmd):
    _, out, err = cli.exec_command(cmd)
    rc = out.channel.recv_exit_status()
    if rc != 0:
        sys.stderr.write(err.read().decode("utf-8", "replace"))
        raise SystemExit(f"comando falhou ({rc}): {cmd}")


def _stream(cli, cmd) -> int:
    chan = cli.get_transport().open_session()
    chan.get_pty()
    chan.exec_command(cmd)
    buf = b""
    while True:
        if chan.recv_ready():
            data = chan.recv(4096)
            if not data:
                break
            buf += data
            while b"\n" in buf:
                line, buf = buf.split(b"\n", 1)
                print(line.decode("utf-8", "replace"))
        elif chan.exit_status_ready():
            break
    if buf:
        print(buf.decode("utf-8", "replace"))
    return chan.recv_exit_status()


if __name__ == "__main__":
    main()
