# internal/scheduler

`scheduler.Service` implements `core.Scheduler` (SPEC §4.14, §5.16) with the
`schedule_list` (`read`), `schedule_create` and `schedule_delete` (`admin`,
so F3 asks an admin each time) tools, all limited to interactive runs so cron
cannot create cron (owner: M8). `Run` is one goroutine sleeping on a single
timer until the earliest `next_run_at` (`ScheduleRepo.NextDue`); it fires each
due schedule through `Runner.Submit` with a fresh session per run (titled
"<name> · <local date>") unless the schedule targets a session, delivers via
`Hub.StreamTo(deliver or PrincipalChat, "final")` (a teammate's DM, a
channel's chat; an account's schedule stays on the web), records a `skipped` run when
the target session's queue is full, and folds `run.finished` events back into
the row: a success resets the failure count and the third consecutive failure
disables the schedule, audits it and tells a teammate's or channel's chat, or
an account in the last task's chat (the chat its tick made when the task never
started, a new one when the tick failed before making one).
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
in sync with `scheduler.heartbeat`, in the owner's time zone, on start, on
every config change and on `ResyncHeartbeat` (the owner changed zone); each
tick inlines `HEARTBEAT.md`, skips without an LLM call when it is blank or when
today's spend of the schedule's runs reaches `budgets.heartbeat_daily_usd` (0
= no cap), and holds the run's events back so a reply starting with
`HEARTBEAT_OK` is never delivered. `Upsert` gives a schedule without a zone
the viewer's own, validates everything (spec, zone,
active hours, limits, a target session of the same principal), ignores caller
values for run state, computes `next_run_at` and audits
`schedule.create`/`update`/`delete`; `RunNow` audits `schedule.run` with the
user who pressed it, since the run itself is the owner's. A
`schedule_create` card without a set chat names the owner and where results
go (`owner`, `owner_kind`, `usual_chat`), and the tool's result tells the
model where runs post. Delivery is confined to the principal's own chats: a
teammate's DM or a channel's own chat; `deliver: web` is for accounts only.
The target is checked again at every fire, so a chat no longer that
teammate's falls back to web only. Ticks of a disabled user start nothing; a
team schedule asks `Hub.TeamActive` first, and a blocked teammate, one no
longer in the team, a membership verdict too old to renew or a channel
turned off skip the tick without counting a failure;
run limits a schedule leaves at zero come from `budgets.scheduled`. If a fired
tick's row update fails, the loop retries the write rather than firing the
tick again, and owner notices are sent after the scheduler lock is released.
`Preview` lists upcoming times and `Describe` renders a spec as "Weekdays at
08:00 (Europe/London)".
