# exec_deviation — 实盘执行偏差观察脚本

LEARN-ga-rl-bayesian §12.1-① / §12.4-② 的离线分析件:实盘成交价 vs 派发时刻
决策 close 的偏差分布 + LIMIT IOC 未成交(EXPIRED)率,检验回测
`close×(1±slippage_bps)` 成交假设;为 backlog-6 方案 A(per-order 价格守卫)
和 cap 校准提供数据。完整说明见 `exec_deviation.py` 文件头 docstring。

```bash
# 用 optuna_toy 的 venv(依赖相同),或: python -m venv .venv && pip install -r requirements.txt
../optuna_toy/.venv/bin/python exec_deviation.py \
    --environment mainnet --symbol BTCUSDT --since 2026-06-15 --csv out.csv
```

**纪律(§12.1-① 使用边界):** `--environment` 必填且烙在每行输出上;
testnet/dev 样本只能验证脚本管线,**统计结论只从 mainnet 样本出**——
testnet 偏差是薄盘+价源分离(klines=mainnet 价,成交=testnet 盘口)的假象。

管线验证记录:2026-06-11 对 dev DB(testnet 历史 59 单/288 成交)跑通——
参考价 join 正确(B2 IOC 单 +2.5bps 对上手算)、薄盘扫单极值(+2505bps)与
backlog-6 判定一致、陈旧参考(>15min)正确剔除。
