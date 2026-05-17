# AGENT SKILLS

This directory defines the canonical roles for AI-assisted work on QuantLab. Each file describes one role: scope of responsibility, which baseline documents it owns, and where its authority ends.

Use these roles to frame conversations: when a question touches GA semantics, the **quant-math-expert** has the final word; when it touches package layout or HTTP API, the **go-backend-expert** does. Cross-role tradeoffs are resolved by the **system-architect**.

| Role | File | Owns |
|---|---|---|
| 系统架构师 — system architect | [system-architect.md](system-architect.md) | Two-layer boundary, module duties, ironclad rules |
| 量化数学专家 — quant math expert | [quant-math-expert.md](quant-math-expert.md) | GA semantics, fitness formulas, Sigmoid micro engine, Crucible window scoring |
| Go 后端专家 — Go backend expert | [go-backend-expert.md](go-backend-expert.md) | Package layout, GORM models, HTTP/WS handlers, concurrency primitives |
| 部署与运维专家 — devops expert | [devops-expert.md](devops-expert.md) | Docker, TimescaleDB, observability, migrations |
| 数据工程师 — data engineer | [data-engineer.md](data-engineer.md) | Binance archive ingestion, K-line storage, gap detection (Phase 1.5) |

## Authority chain when roles disagree

```
quant-math-expert  ──┐
go-backend-expert  ──┼──► system-architect  ──► baseline docs (frozen)
devops-expert      ──┘
data-engineer      ──┘
```

The frozen baseline documents — `进化计算引擎.md`, `进化计算引擎_数据契约.md`, `进化计算引擎_Go_struct_草案.md` — outrank every role. No agent may amend them inline; deviations require a versioned amendment in `docs/`.
