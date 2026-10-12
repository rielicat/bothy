# wire

Shared plumbing for the provider clients. `Options` carries a client's id,
kind, base URL, egress-provided `*http.Client` and credential callbacks;
`NewRequest` puts keys in headers only and `Do` turns non-2xx responses into
`core.ProviderError` through a per-vendor classifier (with `Retry-After`,
redacted and length-capped messages), while transport failures become
`network` unless the caller's context ended, in which case its cause (for
example `core.ErrStopped`) is returned; a successful body is cut off at
256 MiB. `SSE` reads server-sent events with an 8 MiB per-event bound;
`Builder` assembles the assistant message in stream order, synthesizes
`call_<n>` ids, repairs tool arguments (`RepairArgs`: fences, double
encoding, trailing commas, unclosed strings and brackets) and drops calls
that never finished and, when the reply stopped at `max_tokens`, calls whose
arguments were empty or had to be closed (`ArgsComplete`), so a truncated
argument is never executed. `wiretest/` is the test
server and event recorder the client tests share.
