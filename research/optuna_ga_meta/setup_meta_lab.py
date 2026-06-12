"""
setup_meta_lab.py — optuna_ga_meta 的一次性 scratch 环境搭建(README §3)。

与生产完全隔离的代价链(为什么长这样):
  - SharpeBank 污染 ⇒ 不能用生产 saas 实例(README §3);
  - 生产 PG 数据目录在 /var,该分区 2026-06-12 实测 100% 满(剩 14MB),
    任何成规模写入都会 DiskFull;tablespace 目录须归 postgres OS 用户,
    无 root 做不到 ⇒ 也不能用生产 PG 实例;
  - ⇒ 在 /home(大分区)以当前用户起**独立 PG 实例 :5433**,meta 实验
    (quantlab_meta 库 + optuna_ga_meta storage)全住这里。
    可整目录 rm -rf 重来;生产实例全程只读(SELECT/COPY TO)。

幂等步骤:
  1. initdb /home/l9g/quantlab-meta-pg/data(:5433,listen 127.0.0.1,
     socket /tmp,shared_preload_libraries=timescaledb,本地 trust);
  2. pg_ctl start(已在跑则跳过);
  3. 建库 quantlab_meta;schema 用 pg_dump -s(klines/kline_gaps),
     先 create_hypertable(镜像 store/db.go 参数)再灌数据——server 启动时
     的 create_hypertable(if_not_exists) 即成 no-op;
  4. 数据:生产 → meta 流式 COPY;klines 经 DISTINCT ON 去重(生产
     hypertable root 堆残留 9805 行,31 键与 chunk 重复,全在 1m;
     `pg_dump -t` 对 hypertable 只导根表,实测 76k 行丢成 9.8k,故弃用);
  5. 生成 config.metaopt.yaml:db→127.0.0.1:5433/quantlab_meta,
     端口→:8090/:8091/:9092。
"""
import os
import subprocess
import sys
import time
from pathlib import Path

import psycopg
import yaml

PROD_CONFIG = Path("/home/l9g/quantlab/config.yaml")
META_CONFIG = Path(__file__).parent / "config.metaopt.yaml"
PGBIN = Path("/usr/lib/postgresql/17/bin")
PGROOT = Path.home() / "quantlab-meta-pg"
PGDATA = PGROOT / "data"
PGLOG = PGROOT / "pg.log"
META_PORT = 5433
META_DB = "quantlab_meta"
HTTP, WS, METRICS = ":8090", ":8091", ":9092"
CHUNK_MS = 604_800_000  # store/db.go create_hypertable 镜像


def run(cmd, **kw):
    r = subprocess.run(cmd, **kw)
    if r.returncode:
        sys.exit(f"failed: {' '.join(map(str, cmd))}")
    return r


def ensure_instance(user: str):
    if not PGDATA.exists():
        PGROOT.mkdir(exist_ok=True)
        run([PGBIN / "initdb", "-D", PGDATA, "-U", user, "--auth=trust",
             "-E", "UTF8"], stdout=subprocess.DEVNULL)
        with (PGDATA / "postgresql.conf").open("a") as f:
            f.write(f"\nport = {META_PORT}\nlisten_addresses = '127.0.0.1'\n"
                    "unix_socket_directories = '/tmp'\n"
                    "shared_preload_libraries = 'timescaledb'\n")
        print(f"initdb: {PGDATA}")
    st = subprocess.run([PGBIN / "pg_ctl", "-D", PGDATA, "status"],
                        stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    if st.returncode != 0:
        run([PGBIN / "pg_ctl", "-D", PGDATA, "-l", PGLOG, "start"],
            stdout=subprocess.DEVNULL)
        time.sleep(1.5)
        print(f"pg started on :{META_PORT} (log: {PGLOG})")
    else:
        print(f"pg already running on :{META_PORT}")


def main():
    cfg = yaml.safe_load(PROD_CONFIG.open())
    db = cfg["database"]
    prod_dsn = (f"host={db['host']} port={db['port']} user={db['user']} "
                f"password={db['password']} dbname={db['database']} "
                f"sslmode={db.get('ssl_mode', 'disable')}")
    ensure_instance(db["user"])

    meta_admin = f"host=127.0.0.1 port={META_PORT} user={db['user']} dbname=postgres"
    with psycopg.connect(meta_admin, autocommit=True) as conn:
        if conn.execute("SELECT 1 FROM pg_database WHERE datname=%s",
                        (META_DB,)).fetchone():
            print(f"db {META_DB}: exists")
        else:
            conn.execute(f'CREATE DATABASE "{META_DB}"')
            print(f"db {META_DB}: created")

    meta_dsn = meta_admin.replace("dbname=postgres", f"dbname={META_DB}")
    src_sql = {
        "klines": "SELECT DISTINCT ON (symbol, interval, open_time) * "
                  "FROM klines ORDER BY symbol, interval, open_time",
        "kline_gaps": "SELECT * FROM kline_gaps",
    }
    tables = list(src_sql)
    with psycopg.connect(prod_dsn) as src:
        want = {t: src.execute(f"SELECT count(*) FROM ({src_sql[t]}) q").fetchone()[0]
                for t in tables}

    with psycopg.connect(meta_dsn) as conn:
        have = {}
        for t in tables:
            ok = conn.execute(
                "SELECT 1 FROM information_schema.tables WHERE table_name=%s",
                (t,)).fetchone()
            have[t] = (conn.execute(f"SELECT count(*) FROM {t}").fetchone()[0]
                       if ok else None)

    if all(have[t] == want[t] for t in tables):
        print(f"klines/kline_gaps in {META_DB}: row counts match prod "
              f"({want}), skip copy")
    else:
        env = {**os.environ, "PGPASSWORD": db["password"]}
        with psycopg.connect(meta_dsn, autocommit=True) as conn:
            for t in tables:
                conn.execute(f"DROP TABLE IF EXISTS {t} CASCADE")
        dump = subprocess.Popen(
            [PGBIN / "pg_dump", "-h", db["host"], "-p", str(db["port"]),
             "-U", db["user"], "-s"] + [a for t in tables for a in ("-t", t)]
            + [db["database"]],
            stdout=subprocess.PIPE, env=env)
        run([PGBIN / "psql", "-h", "127.0.0.1", "-p", str(META_PORT),
             "-U", db["user"], "-d", META_DB, "-q", "-v", "ON_ERROR_STOP=0"],
            stdin=dump.stdout, env=env,
            stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        dump.wait()
        with psycopg.connect(meta_dsn, autocommit=True) as conn:
            conn.execute("CREATE EXTENSION IF NOT EXISTS timescaledb")
            conn.execute(
                "SELECT create_hypertable('klines','open_time', "
                "if_not_exists=>TRUE, chunk_time_interval=>%s::bigint)",
                (CHUNK_MS,))
        with psycopg.connect(prod_dsn) as src, psycopg.connect(meta_dsn) as dst:
            for t in tables:
                with src.cursor().copy(
                        f"COPY ({src_sql[t]}) TO STDOUT (FORMAT binary)") as out, \
                     dst.cursor().copy(
                        f"COPY {t} FROM STDIN (FORMAT binary)") as inp:
                    for chunk in out:
                        inp.write(chunk)
            dst.commit()
            got = {t: dst.execute(f"SELECT count(*) FROM {t}").fetchone()[0]
                   for t in tables}
        if got != want:
            sys.exit(f"copy mismatch: want {want}, got {got}")
        print(f"copied: {got}")

    cfg["database"]["host"] = "127.0.0.1"
    cfg["database"]["port"] = META_PORT
    cfg["database"]["database"] = META_DB
    cfg["server"]["http_listen"] = HTTP
    cfg["server"]["ws_listen"] = WS
    cfg["server"]["metrics_listen"] = METRICS
    META_CONFIG.write_text(yaml.safe_dump(cfg, allow_unicode=True, sort_keys=False))
    print(f"wrote {META_CONFIG} (db=127.0.0.1:{META_PORT}/{META_DB}, http={HTTP})")
    print("next: go build -o /tmp/quantlab-saas ./cmd/saas && "
          f"/tmp/quantlab-saas --config {META_CONFIG}")


if __name__ == "__main__":
    main()
