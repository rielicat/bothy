# internal/sandboxd/enginetest

An in-memory `engine.API` for tests of sandboxd and its gateway client. It
keeps containers as records and runs each exec as a small scripted program
behind pipes that speak the engine's multiplexed stream, so the real exec,
stdio, kill, eviction and self-test paths of sandboxd run end to end without
Docker. The fake `sbx-init selftest` dials sandboxd's canary socket to prove
the egress relay check; `FailChecks`, `Survivors`, `Program` and `Vanish`
script failures. For the bind-mount mode, `ContainerStart` resolves bind
sources on this machine (following symlinks, as the kernel does), the fake
`sbx-init mountids` reports what they resolved to, `Volumes` sets what
`VolumeInspect` returns and `OnStart` can change the files right after. Test support only: nothing in production imports it.
