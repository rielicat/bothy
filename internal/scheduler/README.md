# internal/scheduler

`scheduler.Service` implements `core.Scheduler` (SPEC §4.14, §5.16) with the
`schedule_list` (`read`), `schedule_create` and `schedule_delete` (`admin`,
so F3 asks an admin each time) tools, all limited to interactive runs so cron
cannot create cron (owner: M8). `Run` is one goroutine sleeping on a single
timer until the earliest `next_run_at` (`ScheduleRepo.NextDue`); it fires each
due schedule through `Runner.Submit` with a fresh session per run (titled
"<name> · <local date>") unless the schedule targets a session, delivers via
`Hub.StreamTo(deliver or PreferredChat, "final")`, records a `skipped` run when
the target session's queue is full, and folds `run.finished` events back into
the row: a success resets the failure count and the third consecutive failure
disables the schedule, audits it and tells the owner in their preferred DM.
Cron specs (5 fields, parsed by gronx) are matched in wall-clock space and
mapped back to instants with ISC cron's DST rules: a fixed-time job whose time
is skipped by spring-forward runs once when the clocks change and never runs
twice in the repeated autumn hour, while wildcard jobs follow the wall clock.
Heartbeats run every interval ±10% inside their active hours; `once` takes RFC
3339 or a local date-time. A tick more than a minute late counts as missed:
`missed_run: skip` reschedules without a trace, `once` fires a single catch-up
when the newest missed occurrence is under an hour old. While `SetPaused`, the
runner's kill switch or `scheduler.enabled: false` is on, due ticks are skipped,
not replayed. The owner's `heartbeat` schedule (`created_by: system`) is kept
in sync with `scheduler.heartbeat` on start and on every config change; each
tick inlines `HEARTBEAT.md`, skips without an LLM call when it is blank or when
today's spend of the schedule's runs reaches `budgets.heartbeat_daily_usd` (0
= no cap), and holds the run's events back so a reply starting with
`HEARTBEAT_OK` is never delivered. `Upsert` validates everything (spec, zone,
active hours, limits, a target session of the same principal, delivery only to
the owner's own DMs or spaces), ignores caller values for run state, computes
`next_run_at` and audits `schedule.create`/`update`/`delete`. `Preview` lists
upcoming times and `Describe` renders a spec as "Weekdays at 08:00
(Europe/London)".
