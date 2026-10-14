# internal/skills/treehash

`treehash.Sum` fingerprints a directory tree for pinning: a sha256 over every
entry in lexical order, each recorded as its length-prefixed slash path, file
type and permission bits, and the sha256 of its contents (regular files) or
its link target (symlinks, which are never followed). Devices, sockets and
pipes are refused. Skills pin `SKILL.md` trees with it and `internal/mcp`
pins MCP install trees (`mcp_installs.tree_hash`), so both modules detect
drift the same way.

`SumRootFiles` is the same walk handing each regular file's hash, size and
the bytes it hashed to a callback: the skill library takes its approved
snapshot from it, so the copy and the pinned hash describe the same tree.
