// Package wire holds what the hand-written provider clients share: the
// client options, an SSE reader, HTTP error classification into
// core.ProviderError, tool-argument repair and the stream accumulator that
// assembles a core.Response from streamed events. It never retries and never
// reads the environment.
package wire
