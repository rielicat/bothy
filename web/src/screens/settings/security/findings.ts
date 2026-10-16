// Plain sentences for the findings of the security audit and the health
// check (docs/design/ui.md §11.6, §11.7). The server's titles name config
// keys and components, so known findings get company-voice copy here; an
// unknown one falls back to the server title (flagged for the server, S2 style).
import type { Finding, Severity } from '../../../lib/api/types.gen';

export type FindingAction = 'fix' | 'how' | 'running-code' | 'unlock';

export interface FindingCopy {
  text: string;
  /** Plain steps behind "How to fix". */
  steps?: string;
  action: FindingAction;
}

const copy: Record<string, { text: string; steps?: string; action?: FindingAction }> = {
  'vault.kek_colocated': {
    text: 'The key that protects saved keys is stored next to the database.',
    steps: 'Keep the key on its own volume: add a separate keys volume to the compose file, move the key there and restart Bothy. A copy of the data folder then no longer includes it.',
  },
  'vault.kek_env': {
    text: 'The key that protects saved keys comes from a setting other programs on this server can read.',
    steps: 'Move the key into a file or a Docker secret, remove it from the environment and restart Bothy.',
  },
  'vault.sealed': { text: 'Saved keys are locked.', action: 'unlock' },
  'sandbox.unavailable': { text: 'Running code is off on this server.', action: 'running-code' },
  'sandbox.not_isolated': {
    text: "The isolated workspace failed its safety check, so Bothy won't run code.",
    action: 'running-code',
  },
  'sandbox.rootful_runc': {
    text: 'Code runs with weaker isolation than recommended.',
    steps: 'Run the workspace service on rootless Docker, then restart Bothy.',
  },
  'sandbox.bwrap_limits': {
    text: "Commands Bothy runs have no memory or process limits on this server.",
    steps: 'Start Bothy with the compose file from the deployment guide, which limits memory and processes for each workspace.',
  },
  'data.permissions': {
    text: 'Data files can be read by other users on this server.',
    steps: 'Make the data folder readable only by the user Bothy runs as.',
  },
  'server.exposure': {
    text: 'Bothy may be reachable from other computers without HTTPS.',
    steps: 'Put Bothy behind HTTPS, with a reverse proxy or its own certificate, before people use it from other computers.',
  },
  'server.insecure_lan': {
    text: 'Bothy accepts plain HTTP from your local network.',
    steps: 'Turn on HTTPS, or only allow plain HTTP from this computer.',
  },
  'tokens.admin_scope': {
    text: 'Some API tokens can change settings.',
    steps: 'Replace them with tokens that can only chat and read, in Account → API access.',
  },
  'tokens.expired_unrevoked': {
    text: "Some expired API tokens haven't been revoked.",
    steps: 'Revoke them in Account → API access.',
  },
  'policy.hands_off': {
    text: 'Bothy asks only before deleting, deploying or spending money.',
    steps: 'Choose Careful or Balanced under Approvals on this page.',
  },
  'policy.auto_external_rules': {
    text: 'Some rules let Bothy send information out without asking.',
    steps: 'Review the rules in Advanced → Rules.',
  },
  'budgets.unlimited': {
    text: "There's no daily spending limit.",
    steps: 'Set one in General → Spending.',
  },
  'egress.wildcards': {
    text: 'Bothy can reach whole groups of websites, not only exact sites.',
    steps: 'Replace the entries that start with *. with the exact sites you need, in Websites Bothy can reach.',
  },
  'egress.shared_tenant_allowed': {
    text: 'Bothy can reach a shared hosting service where anyone can publish content.',
    steps: 'Allow only the exact site you trust, in Websites Bothy can reach.',
  },
  'egress.presets_enabled': {
    text: 'Package downloads are on, so running code can reach PyPI or npm.',
    steps: 'Turn them off under Running code unless your team needs them.',
  },
  'mcp.http_plaintext': {
    text: 'Some connected tools are reached over plain HTTP.',
    steps: 'Give them an https:// address in Connections.',
  },
  'skills.changed': {
    text: 'A skill changed and needs a review.',
    steps: 'Review it in Knowledge → Skills.',
  },
  'backups.none_recent': {
    text: "There's no recent backup.",
    steps: 'Use Back up now under Backup, or schedule backups on the server.',
  },
  'telemetry.off': { text: "Bothy doesn't send usage data anywhere." },
  'config.errors': {
    text: "Some settings couldn't be loaded, so Bothy uses safe defaults for them.",
    steps: 'See which ones in Advanced → Configuration file and correct them.',
  },
};

/** Server titles are labels ("Telemetry is off"); the line reads as a sentence. */
const sentence = (s: string) => (/[.?!…]$/.test(s.trim()) ? s.trim() : `${s.trim()}.`);

// auth.sign_in covers three problems; the fix address names the provider and
// the server's title the problem (internal/auth/checks.go).
function signInFinding(f: Finding): FindingCopy {
  const p = f.fix?.includes('github') ? 'GitHub' : 'Google';
  if (f.title.includes('pinned'))
    return { text: 'GitHub organizations are checked by name only.', steps: 'Save the GitHub organizations once in Security → GitHub sign-in so Bothy can pin them.', action: 'how' };
  if (f.title.includes('locked')) return { text: `Saved keys are locked, so ${p} sign-in can't work.`, action: 'unlock' };
  return { text: `${p} sign-in is on, but its client secret is missing.`, steps: `Open Security → ${p} sign-in and save the client secret again.`, action: 'how' };
}

export function describeFinding(f: Finding): FindingCopy {
  if (f.id === 'auth.sign_in') return signInFinding(f);
  const c = copy[f.id];
  const action: FindingAction = c?.action ?? (f.auto_fix ? 'fix' : 'how');
  return { text: c?.text ?? sentence(f.title), steps: c?.steps, action };
}

const order: Severity[] = ['critical', 'high', 'medium', 'low', 'info'];

/** Findings worth someone's attention, worst first; "info" ones are notes, not problems. */
export function needsAttention(list: Finding[] | null | undefined): Finding[] {
  return (list ?? []).filter((f) => f.severity !== 'info').sort((a, b) => order.indexOf(a.severity) - order.indexOf(b.severity));
}

/** Every finding, worst first. */
export function bySeverity(list: Finding[] | null | undefined): Finding[] {
  return [...(list ?? [])].sort((a, b) => order.indexOf(a.severity) - order.indexOf(b.severity));
}
