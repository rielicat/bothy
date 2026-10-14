# internal/sandboxd/engine

A minimal Docker Engine API client over a unix socket (SPEC D11): ping and
version negotiation (requests are pinned to API 1.47, 1.45 is the minimum
for volume subpaths and 1.44 for the bind-mount mode; `LimitAPI` makes a newer
engine stand in for an older one), info, image inspect and digest-only pulls,
volume inspect, container create/start/kill/remove/list/inspect, and exec
create/inspect plus exec start with connection hijacking. `Demux` decodes the engine's multiplexed
stdout/stderr stream. `API` is the interface sandboxd depends on, so tests can
substitute `enginetest.Fake`. It replaces `moby/client`, which would pull in
containerd, OCI and OpenTelemetry for a dozen endpoints.
