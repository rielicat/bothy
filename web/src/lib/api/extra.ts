import type { ModelInfo, ProbeResult } from './types.gen';

// tygo turns an empty trailing "//" comment in internal/core into a line
// comment that swallows the next field, so ModelInfo.tools and
// ProbeResult.context are missing from types.gen.ts although the API sends
// them (ICR: drop the empty comments in core/provider.go).
export type ModelInfoX = ModelInfo & { tools?: boolean };
export type ProbeResultX = ProbeResult & { context?: number };
