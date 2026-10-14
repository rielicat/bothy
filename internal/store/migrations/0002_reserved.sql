-- 0002 is reserved for the v0.2 repositories migration (F4, GitHub), which
-- lands on another branch. The migrator needs versions 1..N without gaps, so
-- this no-op holds the number; delete this file when 0002_* from F4 merges.
SELECT 1;
