# CI 入门:`.github/workflows/ci.yml` 从原理到上下游

> 这是一篇**给接手本项目的开发者**的背景讲解。
> 读完你能回答:**这个仓库的 CI 到底在每次 push / PR 时做了什么、为什么这么做、它读了哪些文件、哪些文件又是为它而存在的、它红了我该怎么查。**
>
> 规范版决策记录在 [`../saas-schema-migration-draft.md`](../saas-schema-migration-draft.md)(§5.1 / D5–D7);
> 为什么会有 goose 这套东西,见姊妹篇 [`LEARN-schema-migration-why-goose.md`](LEARN-schema-migration-why-goose.md)。
> 本文聚焦 **CI 这台"自动守门人"本身**。

---

## 0. 一句话背景

> 这是本仓库**第一个、也是目前唯一一个 CI**(随 goose 迁移方案一起在 PR #6 落地)。
> 它的职责:**每次代码进 main 或开/更新 PR 时,自动起一台干净机器,验证三件事——测试不挂、schema 的两个真相源没有分叉、新写的迁移 SQL 没有危险操作。**

为什么这个仓库在那之前一直没有 CI?因为它长期是单人原型迭代,schema 全靠 GORM `AutoMigrate` 自动建表,"对不对"靠开发者本机跑一遍。直到要给生产补上**正规迁移**(goose),才出现了一个**机器必须自动守住的不变量**:"GORM struct 描述的 schema" 和 "goose 迁移文件生成的 schema" 必须永远一致。人工守不住这个,于是 CI 来了。

记住这条主线:**CI 的存在,90% 是为了守住那道 schema 漂移闸门。** 其余的单元测试 + SQL lint 是顺手搭上的。

---

## 1. 先建立心智模型:GitHub Actions 的五个词

CI 跑在 **GitHub Actions** 上。理解 `ci.yml` 前,先认五个概念,自顶向下:

| 词 | 是什么 | 在 ci.yml 里对应 |
|---|---|---|
| **Workflow(工作流)** | 一个 `.yml` 文件 = 一条完整流水线 | 整个 `ci.yml`,名字叫 `ci`(第 1 行 `name: ci`) |
| **Trigger(触发器)** | 什么事件让流水线跑起来 | `on:` 块(`push` 到 main / 任何 `pull_request`) |
| **Job(作业)** | 流水线里一个**独立、并行**的任务,各跑在自己的虚拟机上 | `test` 和 `lint-migrations` 两个 job,**并行跑** |
| **Runner(运行器)** | 跑 job 的那台一次性虚拟机 | `runs-on: ubuntu-latest`,GitHub 临时分配,跑完销毁 |
| **Step(步骤)** | job 里一条条按顺序执行的命令 | 每个 `- uses:` / `- name: ... run:` |
| **Service(服务容器)** | job 旁边附带起的容器,通常是数据库 | `services.timescaledb`,给 `test` job 提供一个真 Postgres |

关键直觉:
- **两个 job 互不相干、并行跑**——`test` 红不影响 `lint-migrations` 继续,反之亦然。
- **每个 job 都是从零开始的干净机器**:它不知道你本地装了什么,所有依赖(Go、pg_dump、squawk)都得在 step 里自己装。这就是为什么 ci.yml 里有一堆"安装 XXX"的步骤——不是啰嗦,是 runner 上**真的没有**。
- **Service 容器跑完即弃**:那个 Postgres 里的数据不会留到下一次,所以测试可以随便建库、灌数据、不用清理。

---

## 2. 上游:从 `git push` 到流水线跑起来

谁来"调用"这条流水线?没有人手动调——是 **GitHub 监听仓库事件**自动触发的。看 `on:` 块:

```yaml
on:
  push:
    branches: [main]   # 有提交进入 main 分支时
  pull_request:        # 任何 PR 被开/被推新 commit 时
```

所以触发链是:

```
你 git push / 开 PR
        │
        ▼
GitHub 收到事件,匹配 ci.yml 的 on: 规则
        │
        ▼
分配 runner(s) → 并行起 test 和 lint-migrations 两个 job
        │
        ▼
结果回写到那次 commit / 那个 PR 的 "Checks" 区(绿勾 / 红叉)
```

两条触发线各有用途:
- **`pull_request`**:你现在 PR #7 跑的就是这条。**这是主防线**——代码在合进 main *之前*就被验证,坏改动挡在门外。
- **`push: [main]`**:合并之后在 main 上**再跑一次**。为什么合并前跑了还要再跑?因为 PR 分支可能落后于 main,合并产生的最终代码组合此前没被单独验证过(典型的"各自绿、合起来红")。

> 一个容易困惑的点:**ci.yml 本身是被 PR 引入的,它能对引入它的那个 PR 生效吗?** 能——`pull_request` 事件用的是 **PR 合并后的代码视图**里的 workflow 定义,所以一个"新增 CI"的 PR,其 CI 会在该 PR 上就跑起来。PR #6 当初正是这样自我验证的。

---

## 3. 文件全景:ci.yml 引用谁、谁为 ci.yml 存在

CI 不是孤立的一个文件。围绕它有一组**互相引用**的文件,先看全景再逐个拆:

```
.github/workflows/ci.yml ──┬─ 读 ─▶ go.mod                         (定 Go 版本)
   (流水线定义)             │
                           ├─ test job ─ 读 ─▶ .github/ci-config.yaml   (喂给集成测试的配置)
                           │                        │
                           │                        └─ 指向 ─▶ services.timescaledb (临时 PG)
                           │
                           ├─ test job ─ 跑 ─▶ internal/saas/store/migrate_drift_test.go  ★漂移闸门
                           │                        │ 内部调用
                           │                        ├─▶ store.NewDB (AutoMigrate 路径)
                           │                        └─▶ store.RunMigrations → migrate.go → migrations/*.sql
                           │
                           └─ lint-migrations job ─ 读 ─▶ .squawk.toml           (lint 规则)
                                                          └─ 作用于 ─▶ migrations/*.sql
```

一句话归类:
- **ci.yml 主动读取/依赖的**:`go.mod`(Go 版本)、`.github/ci-config.yaml`(测试配置)、`.squawk.toml`(lint 配置)。
- **ci.yml 触发执行的代码**:全量单元测试 + 那个 `//go:build integration` 的漂移测试。
- **纯粹"为 CI 而生"的文件**:`.github/ci-config.yaml`(只有 CI 用它,见 §7)。

---

## 4. Job 1 `test`:测试 + 漂移闸门,逐步拆解

这是承重 job。它每一步都不是随便加的,逐条讲"为什么":

### 4.1 起一个真 Postgres(service 容器)

```yaml
services:
  timescaledb:
    image: timescale/timescaledb:latest-pg17
    env: { POSTGRES_USER: ql, POSTGRES_PASSWORD: pw, POSTGRES_DB: quantlab }
    ports: [ "5432:5432" ]
    options: >- --health-cmd "pg_isready -U ql" --health-interval 5s ...
```

- **为什么要真 Postgres,不能用 sqlite/mock?** 因为漂移闸门要比对 `pg_dump` 的**真实输出**,还涉及 TimescaleDB 的 hypertable、partial unique index 这些 **Postgres/TimescaleDB 专有特性**——这些东西在别的数据库里根本不存在,mock 不出来。(这也呼应仓库约定:repo 测试不准引 sqlite。)
- **为什么是 `latest-pg17`?** 生产用的就是 TimescaleDB pg17。CI 的库必须和生产同版本,否则"在 CI 绿、上生产红"。
- **`--health-cmd`**:容器起来 ≠ Postgres 能连了。健康检查让 runner **等 `pg_isready` 通过**才往下跑,避免测试连到一个还没准备好的库。

### 4.2 装 Go(按 go.mod 的版本)

```yaml
- uses: actions/setup-go@v5
  with: { go-version-file: go.mod }
```

`go-version-file: go.mod` 意思是**别在 yml 里写死版本**,直接读 `go.mod` 里的 `go` 指令。好处:升级 Go 只改一处(go.mod),CI 自动跟上,不会两边版本漂移。

### 4.3 装 PG17 的 `pg_dump`(最容易被忽略的一步)

```yaml
- name: Install PostgreSQL 17 client (pg_dump)
  run: |
    ...PGDG 源... sudo apt-get install -y postgresql-client-17
    echo "/usr/lib/postgresql/17/bin" >> "$GITHUB_PATH"
```

- **为什么单独装?** runner(ubuntu-latest)自带的 `pg_dump` 客户端**版本偏旧**。而 `pg_dump` 有个铁规矩:**客户端版本必须 ≥ 服务端版本**,否则拒绝 dump 新版本的库。服务端是 pg17,所以必须装 pg17 的客户端。
- **`$GITHUB_PATH`**:把新装的 `pg_dump` 目录加到 PATH 最前,**盖过**自带的旧版本。后续 step 调 `pg_dump` 才会命中 17。
- 这一步是纯环境铺垫,但**漏了它整个漂移闸门就跑不起来**——这正是"runner 是干净机器,啥都得自己装"的典型体现。

### 4.4 单元测试

```yaml
- name: Unit tests
  run: go test ./...
```

跑全仓库所有**非 integration** 测试(`//go:build integration` 的默认不编译进来)。这一步覆盖引擎、fitness、策略、API handler 等等的常规测试。

### 4.5 漂移闸门(本 job 的核心)

```yaml
- name: Integration tests (schema drift guard)
  run: go test -tags=integration ./internal/saas/store/ -args -config="$GITHUB_WORKSPACE/.github/ci-config.yaml"
```

- `-tags=integration`:**这才把** `migrate_drift_test.go` 编译进来(它文件头是 `//go:build integration`,平时被排除)。
- `-args -config=...`:把 CI 专用配置(§7)传给测试,告诉它去连哪个 Postgres。
- 这一步干了什么,见下一节专讲。

---

## 5. 漂移闸门的原理(必懂)

这是整个 CI 的灵魂。文件:`internal/saas/store/migrate_drift_test.go`,测试函数 `TestMigrationsMatchAutoMigrate`。

### 5.1 它要守的不变量

项目里 schema 有**两个真相源**:

| 真相源 | 是什么 | 谁用 |
|---|---|---|
| **GORM struct**(`models.go` 的 `AllModels()`) | ORM 映射 + dev/lab 的 `AutoMigrate` 据此建表 | 开发机 |
| **goose 迁移文件**(`migrations/*.sql`) | 生产据此建/改表 | 生产 saas |

两者描述的**必须是同一个 schema**。但它们是两份独立维护的东西——改了 struct 忘了写迁移(或反之),两边就**悄悄分叉**,而且要到生产才暴露。漂移闸门就是把这俩**钉死**的那颗钉子。

### 5.2 它怎么做到

测试逻辑(精简版):

```
1. 连上 CI 的 Postgres(superuser),建两个一次性空库:qldrift_am_<纳秒>、qldrift_gs_<纳秒>
2. 对 am 库:store.NewDB(app_role=dev) → 走 AutoMigrate + raw DDL 路径建表
3. 对 gs 库:store.NewDB(app_role=saas) → 走 goose 迁移路径建表
4. 各跑 pg_dump --schema-only --no-owner --no-privileges(排除 goose 自己的版本表)
5. 归一化(去注释/SET/空行)后,断言两份 dump 逐字节相同
6. t.Cleanup:先 pg_terminate_backend 再 DROP 两个库
```

> 注意第 2、3 步:测试是通过 **`app_role` 切换迁移路径**的。在 ⑤(`migration_mode`)落地后,`app_role=saas` + 空 `migration_mode` 仍然解析成 goose 路径,所以这个测试照常成立。

**为什么 byte-identical 才算过?** 因为只要两条路径建出的表有**任何**结构差异(少一列、类型不同、索引谓词写法不同、约束缺失……),dump 出来就不一样。逐字节相等 = 两个真相源在结构上完全等价。

### 5.3 这就是为什么改 struct 必须配迁移

接手后你最该记住的一条:**动了 `models.go`(加表/加列/改类型),就必须写一支对应的 `migrations/NNNNN_*.sql`,否则这个测试红,PR 进不去。** 反过来,迁移文件偏离了 struct 也一样红。这道闸门把"忘了同步"这类人为疏漏,从"生产事故"降级成了"CI 红叉"。

---

## 6. Job 2 `lint-migrations`:Squawk 静态检查迁移 SQL

第二个 job 跟数据库无关,纯静态分析:

```yaml
- name: Install Squawk
  run: curl ... squawk-linux-x64 ; chmod +x ...
- name: Lint migrations
  run: squawk -c .squawk.toml --no-error-on-unmatched-pattern internal/saas/store/migrations/*.sql
```

- **Squawk 是什么?** 一个**迁移 SQL 的危险操作 linter**。它读你的 DDL,警告那些在**有数据的生产表**上会出事的写法,比如:
  - `ADD COLUMN ... NOT NULL` 不带 `DEFAULT` → 全表重写 + 长时间锁。
  - `int` 改 `bigint` → 重写整列。
  - 新建索引不用 `CONCURRENTLY` → 写锁阻塞流量。
- **配置 `.squawk.toml`**(ci.yml 通过 `-c` 引用):
  - `excluded_paths = ["*_baseline.sql"]`:`00001_baseline.sql` 是"从空库一次性建全表",Squawk 那些"针对已有大表"的规则在它身上全是误报,所以**豁免**;它的正确性由 §5 的漂移闸门保证,不归 Squawk 管。
  - `assume_in_transaction = true`:goose 默认把每个迁移包在事务里,让 Squawk 按"在事务内"判规则。
  - `pg_version = "17.0"`:让版本相关的规则按 pg17 判断。
- **`--no-error-on-unmatched-pattern`**:目前 `migrations/` 里只有被豁免的 baseline,glob 实际匹配不到要 lint 的文件。这个 flag 让"没东西可 lint"也算绿。等你写了 `00002_*.sql`,Squawk 就**真正开始发力**了。

> 所以现在这个 job 基本是"空转待命"状态——它是为**未来的增量迁移**铺好的防线,不是现在就有活干。

---

## 7. 配套文件 `.github/ci-config.yaml`

```yaml
app_role: dev
database: { host: localhost, port: 5432, user: ql, password: pw, database: quantlab, ... }
jwt: { secret: ci-only-jwt-secret-at-least-thirty-two-bytes-long }
ga: { pop_size: 10, max_generations: 2 }
```

- **它是干嘛的?** §4.5 的漂移测试需要一份 `config.Config` 才能跑(它调 `config.Load`)。这个文件就是喂给它的——**指向 §4.1 那个临时 Postgres**(host/port/账号密码正好对上 service 容器的 env)。
- **只有 CI 用它**:文件头第一行就写明"Config consumed ONLY by the integration tests in CI"。里面的账号密码是公开的 CI 测试值,**绝不是任何真实部署的凭证**。
- `app_role: dev` + 缩小的 `ga` 参数:测试本身不跑 GA,这些只是让 `config.Validate` 通过的最小合法值。

---

## 8. 把一切串起来:一次 PR 的完整时序

```
开发者改了代码(比如给某个 GORM struct 加了一列)
        │  git push 到 PR 分支
        ▼
GitHub 触发 ci.yml(pull_request 事件)
        │
        ├──────────────── Job test(runner A) ────────────────┐
        │  起 timescaledb 容器 → 装 Go(读 go.mod)→ 装 pg17    │
        │  客户端 → go test ./...(单元)                        │
        │  → go test -tags=integration(漂移闸门):             │
        │       建两库 → AutoMigrate vs goose → pg_dump 比对   │
        │       ← 若 struct 改了没配迁移 → 这里红 ✗            │
        └──────────────────────────────────────────────────────┘
        │
        ├──────────── Job lint-migrations(runner B,并行)──────┐
        │  装 squawk → 按 .squawk.toml lint migrations/*.sql    │
        │  (当前只有被豁免的 baseline → 绿)                    │
        └──────────────────────────────────────────────────────┘
        │
        ▼
两个 job 结果汇总到 PR 的 Checks:全绿才好合并
        │  合并进 main
        ▼
push:[main] 再触发一次同样的 ci.yml(合并后代码的最终验证)
```

---

## 9. CI 红了怎么办

按 job 对症下药:

**`test` job 红:**
- *单元测试红*:`go test ./...` 本地直接复现,跟 CI 无关,正常修。
- *漂移闸门红*:几乎总是因为**改了 `models.go` 没写匹配的迁移**(或迁移写偏了)。本地复现(需要一个能 `CREATE DATABASE` 的 Postgres + PATH 上有 pg_dump):
  ```bash
  go test -tags=integration ./internal/saas/store/ \
      -run TestMigrationsMatchAutoMigrate -args -config=/abs/your-config.yaml
  ```
  它会打印两份 dump 的 diff——差在哪,你的迁移就缺哪一块,补上 `migrations/NNNNN_*.sql` 直到 diff 为空。详细写迁移的 checklist 见 [`../../internal/saas/store/migrations/README.md`](../../internal/saas/store/migrations/README.md)。

**`lint-migrations` job 红:**
- 你新写的 `00002+` 迁移踩了危险 DDL 规则。本地复现:
  ```bash
  squawk -c .squawk.toml internal/saas/store/migrations/00002_xxx.sql
  ```
  按它的提示改(加 `DEFAULT`、拆 expand/contract、用 `CONCURRENTLY` 并声明 `-- +goose NO TRANSACTION` 等)。

> 本机跑漂移测试的环境坑(端口冲突、磁盘、容器 PGDATA 挂载位置)记录在团队 memory / draft 文档里,真要本地复现时先翻一下,能省很多时间。

---

## 10. 接手者注意事项 / 已知边界

1. **Squawk 有盲区,不能全信。** 它不懂本仓的 TimescaleDB 特性:hypertable 上的 `ALTER` 会广播到所有 chunk、`create_hypertable` 语义、chunk interval、压缩策略——这些 `pg_dump --schema-only` 也看不到(所以漂移测试同样盲),需要人工查 `timescaledb_information.dimensions` 核对。详见 migrations/README 的"Squawk 盲区"小节。
2. **漂移测试只比对 schema 结构,不比对数据、不比对 hypertable 的 chunk 配置。** 它证明"表长一样",不证明"分区策略一样"。
3. **CI 用的是 `latest-pg17` 这个浮动 tag。** 哪天 TimescaleDB 发新的 pg17 镜像,CI 会悄悄换底座。要严格复现历史结果,得钉死具体版本——目前为了省事没钉。
4. **没有 lint/格式门(gofmt/staticcheck/govulncheck)。** 当前 CI 只管"测试 + schema + 迁移 SQL",不管代码风格和静态检查。这些目前靠开发者本地自觉(见代码审阅 memory)。要加的话,就是往 `test` job 里再插几个 step,或开第三个 job。
5. **`migration_mode`(⑤)之后,迁移路径不再等同于 `app_role`。** 但漂移测试仍用 `app_role=saas` 来**强制走 goose 路径**做比对——因为 `saas + 空 migration_mode` 默认解析成 goose。如果将来改了这个默认推导逻辑,记得回头看这个测试还成不成立。

---

## 一句话总结

> `ci.yml` 是 goose 迁移方案的**自动守门人**:PR 一开就在干净机器上跑,核心是那道**漂移闸门**——用一个真 Postgres 分别按 AutoMigrate 和 goose 建库、`pg_dump` 逐字节比对,确保"struct 真相源"和"迁移文件真相源"永不分叉;外加单元测试和迁移 SQL 的危险操作 lint。它把"改 struct 忘了写迁移"这类原本要到生产才爆的错,挡在了合并之前。
