// The final route list (docs/design/ui.md §4.5, §18.3 C10): every page once,
// at 1280 × 800 and 360 × 780 in light and dark, with every G4 probe enforced.
// The chunk specs capture the states; this one proves the whole app together.
//
//   npm run build && SHOTS=1 npx playwright test e2e/shots/all.spec.ts
import { shots, type ShotState } from '../lib/shots';

const page = (name: string, route: string, extra: Partial<ShotState> = {}): ShotState => ({ name, route, ...extra });
// Setup, sign in and the first password have their own centred column (§5.2).
const outside = { probes: { leftEdge: false } };

const states: ShotState[] = [
  page('setup', '/setup', { mock: 'setup', ...outside }),
  page('login', '/login', { mock: 'logged-out', ...outside }),
  page('password', '/password', { ready: 'Choose a new password', ...outside }),
  page('chat-new', '/chat'),
  page('chat', '/chat/ses_01jtrip000000000000000000'),
  page('approvals', '/approvals'),
  page('approvals-done', '/approvals?tab=done'),
  page('approvals-item', '/approvals/apr_01jshell00000000000000000'),
  page('knowledge', '/knowledge'),
  page('knowledge-search', '/knowledge/search?q=redirect'),
  page('knowledge-memory', '/knowledge/memory'),
  page('knowledge-skills', '/knowledge/skills'),
  page('knowledge-skill', '/knowledge/skills/release-notes'),
  page('knowledge-project', '/knowledge/projects/acme/web'),
  page('activity', '/activity'),
  page('activity-task', '/activity/tasks/run_01jtrip1000000000000000'),
  page('activity-schedules', '/activity/schedules'),
  page('activity-schedule', '/activity/schedules/sch_01jbrief00000000000000000'),
  page('activity-schedule-new', '/activity/schedules/new'),
  page('activity-checkin', '/activity/schedules/checkin'),
  page('activity-releases', '/activity/releases'),
  page('activity-spending', '/activity/spending'),
  page('activity-audit', '/activity/audit'),
  page('settings-list', '/settings', { only: { viewports: ['phone'] } }),
  page('settings-account', '/settings/account'),
  page('settings-general', '/settings/general'),
  page('settings-personality', '/settings/general/personality'),
  page('settings-models', '/settings/models'),
  page('settings-connections', '/settings/connections'),
  page('settings-slack', '/settings/connections/slack'),
  page('settings-telegram', '/settings/connections/telegram'),
  page('settings-discord', '/settings/connections/discord'),
  page('settings-github', '/settings/connections/github'),
  page('settings-search', '/settings/connections/search'),
  page('settings-mcp', '/settings/connections/mcp-docs'),
  page('settings-people', '/settings/people'),
  page('settings-security', '/settings/security'),
  page('settings-permissions', '/settings/security/permissions'),
  page('settings-websites', '/settings/security/websites'),
  page('settings-keys', '/settings/security/keys'),
  page('settings-advanced', '/settings/advanced'),
  page('not-found', '/nope'),
];

shots('all', states);
