# internal/budget

`budget.Service` implements `core.Budget` (SPEC §4.11, §5.12, §5.13). It meters
each run (`StartRun` returns a `core.RunMeter` for steps, tokens, wall time
excluding approval waits, and cost), enforces the daily caps
(`budgets.daily_usd` for the whole instance, `per_user_daily_usd[username]`
overrides, `member_daily_usd` for members in multi mode; owners and admins are
bounded by the global cap only), writes `spend_daily` through `UsageRepo`,
records out-of-run calls (`RecordSpend`), publishes `budget.warning` plus a
`budget_80` banner the first time a cap passes `budgets.warn_ratio` on a spend
day (cleared on the next day), rate-limits chat senders with a token bucket per
key (LRU of 10,000 keys) and detects loops over a ring of the last 16 tool
calls (`ObserveTool` with a zero result hash is the pre-execution check).

Spend days are local dates in the configured `timezone`. A per-user or member
cap that is not set is unlimited; an explicit cap of 0 allows no spend, while
`daily_usd: 0` keeps its documented meaning of "no global cap". Unknown prices
skip the dollar checks but never the token checks. Every limit is read from
`config.View` at use, so hot reloads apply to the next check.
