// F1 baseline: every v0.1 screen under the v0.2 tokens, at its v0.2 address,
// so later chunks have a before for their after. v0.1 layouts don't meet the
// G4 probes yet, so they are recorded (index.html), not enforced.
//
//   npm run build && SHOTS=1 npx playwright test e2e/shots/baseline.spec.ts
import { shots, type ShotState } from '../lib/shots';

const states: ShotState[] = [
  { name: 'setup-code', route: '/setup', mock: 'setup' },
  { name: 'setup-account', route: '/setup#t=demo-setup-token', mock: 'setup', ready: 'Create your account' },
  { name: 'login', route: '/login', mock: 'logged-out' },
  { name: 'chat-new', route: '/chat' },
  { name: 'chat', route: '/chat/ses_01jtrip000000000000000000', ready: 'gentle plan' },
  // Not sealed: C1's shell blocks the app with the unlock screen for admins while saved keys are locked.
  { name: 'chat-paused', route: '/chat/ses_01jtrip000000000000000000', mock: 'paused', ready: 'gentle plan' },
  { name: 'approvals', route: '/approvals' },
  { name: 'approvals-parked', route: '/approvals?status=parked' },
  { name: 'knowledge', route: '/knowledge' },
  { name: 'knowledge-memory', route: '/knowledge/memory' },
  { name: 'knowledge-skills', route: '/knowledge/skills' },
  { name: 'activity', route: '/activity' },
  { name: 'activity-task', route: '/activity/tasks/run_01jtrip1000000000000000' },
  { name: 'activity-schedules', route: '/activity/schedules' },
  { name: 'activity-schedule', route: '/activity/schedules/sch_01jbrief00000000000000000' },
  { name: 'activity-schedule-new', route: '/activity/schedules/new' },
  { name: 'activity-releases', route: '/activity/releases' },
  { name: 'activity-spending', route: '/activity/spending' },
  { name: 'activity-audit', route: '/activity/audit' },
  { name: 'settings-account', route: '/settings/account' },
  { name: 'settings-general', route: '/settings/general' },
  { name: 'settings-personality', route: '/settings/general/personality' },
  { name: 'settings-models', route: '/settings/models' },
  { name: 'settings-connections', route: '/settings/connections' },
  { name: 'settings-connections-mcp', route: '/settings/connections/mcp-docs' },
  { name: 'settings-connections-github', route: '/settings/connections/github' },
  { name: 'settings-people', route: '/settings/people' },
  { name: 'settings-security', route: '/settings/security' },
  { name: 'settings-security-websites', route: '/settings/security/websites' },
  { name: 'settings-security-keys', route: '/settings/security/keys' },
  { name: 'settings-advanced', route: '/settings/advanced' },
  { name: 'not-found', route: '/nope' },
];

shots('baseline', states, { mode: 'report' });
