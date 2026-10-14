// Seed data of the mock gateway, gathered for tests that compare against it.
// Each value lives in the domain file that serves it (domains/*.ts); shapes
// follow src/lib/api/types.gen.ts and the values are invented but plausible.
export { ago, ahead, ids, member, owner, principals, ZERO } from './domains/core.ts';
export { audit, egress, runDetail, scheduleRuns, schedules, usageRows } from './domains/activity.ts';
export { approvals, recentApprovals } from './domains/approvals.ts';
export { messages, pins, sessions } from './domains/chat.ts';
export { channels, identities, mcp, mcpPins, spaces } from './domains/connections.ts';
export { memory, memoryHistory, skills } from './domains/knowledge.ts';
export { constitution, doctor, grants, sandbox, secrets, vaultStatus } from './domains/security.ts';
export { models, persona, providers, tokens } from './domains/settings.ts';
export { detections } from './domains/setup.ts';
