# internal/skills

`skills.Library` implements `core.Skills` (SPEC §4.14, §5.6, §12.2 M7): it
scans `<skills.dir>/*/SKILL.md` (default `<work_dir>/skills`, mounted
read-only at `/skills` in every box), parses the YAML frontmatter (`name`,
`description`, optional `license`; `allowed-tools` is parsed but advisory) and
pins each skill to a `treehash` of its directory. New skills are `pending`
until an admin enables them; enabling pins the current hash as approved. Any
later drift, found by `Rescan`, `Index`, `skill_load`/`skill_read` or the
doctor check, moves an enabled skill to `changed`: it is excluded, audited as
`skill.changed`, and the `skills_changed` banner is raised until it is
re-enabled or disabled.

`Index` returns enabled, hash-matching skills by name within
`skills.index_budget_tokens` (bytes/3.6 estimate): full descriptions when they
fit, otherwise names only, truncated to the budget. `Load` and `ReadFile`
serve any installed skill for admin review (256 KB cap, `os.Root`, no `..`,
absolute paths or symlinked components); the model-facing tools `skill_load`
(core, `core.Gated` on at least one enabled skill) and `skill_read` (lazy,
gated likewise) require an enabled skill whose files still match. `Install`
unpacks a zip (≤ 5 MB, ≤ 200 files, ≤ 20 MB unpacked, `SKILL.md` at the root
or in one top-level folder) through `os.Root`, refusing unsafe names,
symlinks and special files; files are written 0660 and nothing is ever
executed. `Checks` provides `skills.changed` (fix: disable).
