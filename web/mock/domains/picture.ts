// Bothy's picture (SPEC §8.3 /api/agent/picture): the default or an admin's
// upload, and what changing it does in each chat app that is turned on.
//
// The mock keeps any upload it is given. A PNG, JPEG or WebP comes back as it
// was sent (the server would crop and scale it); anything else, such as the
// placeholder unit tests send for a form, shows a stand-in picture. Refusals
// come from the picture-refused scenario, except the 5 MB cap.
import type { PictureAppResult, PictureInfo } from '../../src/lib/api/types.gen.ts';
import { channelList } from './connections.ts';
import { asMember, body, fail, json, on, onReset, scenario, sleep, type Req, type Res } from './core.ts';
import { state as settings } from './settings.ts';

const custom = scenario('picture-custom', "An admin replaced Bothy's default picture.", 'settings');
const applyFail = scenario('picture-fail', 'Changing the picture fails in Telegram and Discord; picture-fail:<telegram|discord> picks one (Discord says it allows only a few changes an hour).', 'settings');
const refused = scenario('picture-refused', 'Uploads are refused; picture-refused:<not-picture|too-large|too-small> picks why.', 'settings');

// An admin's upload, as far as the mock can tell: a portrait, nothing like the default.
const standIn = {
  type: 'image/svg+xml',
  data: '<svg xmlns="http://www.w3.org/2000/svg" width="512" height="512" viewBox="0 0 512 512"><rect width="512" height="512" fill="#8A4B2A"/><circle cx="256" cy="208" r="88" fill="#F3E3D6"/><path d="M88 512c16-104 88-168 168-168s152 64 168 168Z" fill="#F3E3D6"/></svg>',
};
declare const process: { getBuiltinModule(id: 'node:fs'): { readFileSync(path: string, encoding: 'latin1'): string } };
// The gateway's own default, as the latin1 string res.end sends byte for byte.
// A path, not a URL: under jsdom the global URL isn't one node:fs accepts.
const here = (import.meta as ImportMeta & { dirname: string }).dirname;
const byDefault = { type: 'image/png', data: process.getBuiltinModule('node:fs').readFileSync(`${here}/../../../internal/picture/default.png`, 'latin1') };

const state = { custom: false, version: '', type: '', data: '', n: 0 };
const use = (p: { type: string; data: string } | null) => {
  state.custom = !!p;
  state.type = (p ?? byDefault).type;
  state.data = (p ?? byDefault).data;
  state.version = `v${++state.n}`;
};
onReset(() => {
  state.n = 0;
  use(null);
});

function current(req: Req): PictureInfo & { type: string; data: string } {
  // The scenario stands for an upload made before the page opened, until the next change here.
  if (custom(req) && !state.custom && state.n === 1) return { custom: true, version: 'v0', ...standIn };
  return { custom: state.custom, version: state.version, type: state.type, data: state.data };
}

const refusals: Record<string, string> = {
  'not-picture': "That file isn't a picture Bothy can use. Choose another one.",
  'too-large': 'That picture is too large. Choose one under 8000 pixels on each side.',
  'too-small': 'That picture is too small. Choose one at least 128 pixels on each side.',
};

function sniff(b: string): string {
  if (b.startsWith('\x89PNG\r\n\x1a\n')) return 'image/png';
  if (b.startsWith('\xff\xd8\xff')) return 'image/jpeg';
  if (b.startsWith('RIFF') && b.slice(8, 12) === 'WEBP') return 'image/webp';
  return '';
}

/** The form's file part, as httpapi.readPictureUpload takes it; the raw body otherwise. */
function filePart(raw: string, type: string): string {
  const m = /^multipart\/form-data;.*boundary=(?:"([^"]+)"|([^;]+))/i.exec(type);
  if (!m) return raw;
  for (const part of raw.split(`--${m[1] ?? m[2]}`)) {
    const head = part.indexOf('\r\n\r\n');
    if (head >= 0 && /name="file"/.test(part.slice(0, head))) return part.slice(head + 4).replace(/\r\n$/, '');
  }
  return '';
}

const appWord: Record<string, string> = { slack: 'Slack', telegram: 'Telegram', discord: 'Discord' };

// ChannelHub.ApplyPicture and httpapi.pictureDetail, sorted by app.
async function apply(req: Req): Promise<PictureAppResult[]> {
  const on = channelList(req)
    .filter((c) => c.enabled)
    .sort((a, b) => a.name.localeCompare(b.name));
  if (on.some((c) => c.name !== 'slack')) await sleep(300);
  const failing = applyFail.arg(req);
  return on.map((c): PictureAppResult => {
    const app = appWord[c.name] ?? c.name;
    if (c.name === 'slack') return { app: c.name, result: 'manual', detail: "In Slack, upload it under App icon in Slack's app settings." };
    if (c.state !== 'connected') return { app: c.name, result: 'failed', detail: `${app} isn't connected right now, so its picture wasn't changed. Try again once it's back.` };
    if (failing !== undefined && (!failing || failing === c.name))
      return { app: c.name, result: 'failed', detail: c.name === 'discord' ? 'Discord only allows a few picture changes an hour. Try again later.' : `Couldn't change the picture in ${app}. Try again later.` };
    return { app: c.name, result: 'updated', detail: `Updated in ${app}.` };
  });
}

function adminOnly(req: Req, res: Res): boolean {
  if (!asMember(req)) return true;
  fail(res, 403, 'forbidden', 'admin only');
  return false;
}

const header = (req: Req, k: string) => {
  const v = req.headers[k];
  return Array.isArray(v) ? v.join(', ') : (v ?? '');
};

on('GET', '/api/agent/picture', (req, res, _m, q) => {
  const p = current(req);
  const etag = `"${p.version}"`;
  res.setHeader('Cache-Control', 'private, no-cache');
  res.setHeader('ETag', etag);
  if (header(req, 'if-none-match') === etag) {
    res.statusCode = 304;
    return res.end();
  }
  if (q.get('download') === '1') {
    const name = String(settings.effective.entries.find((e) => e.path === 'agent.name')?.value ?? 'Bothy').replace(/[^\p{L}\p{N}_-]+/gu, '-').replace(/^[-_]+|[-_]+$/g, '') || 'Bothy';
    const ext = { 'image/png': 'png', 'image/jpeg': 'jpg', 'image/webp': 'webp' }[p.type] ?? 'svg';
    res.setHeader('Content-Disposition', `attachment; filename="${name}-picture.${ext}"`);
  }
  res.statusCode = 200;
  res.setHeader('Content-Type', p.type);
  res.end(p.data, 'latin1');
});

on('GET', '/api/agent/picture/info', (req, res) => {
  const { custom, version } = current(req);
  json(res, 200, { custom, version });
});

on('PUT', '/api/agent/picture', async (req, res) => {
  if (!adminOnly(req, res)) return;
  const raw = filePart(await body(req), header(req, 'content-type'));
  if (raw.length > 5 << 20) return fail(res, 413, 'bad_request', 'That picture is over 5 MB. Choose a smaller one.');
  if (!raw) return fail(res, 400, 'bad_request', 'Choose a picture to upload.');
  const why = refused.arg(req);
  if (why !== undefined) return fail(res, 400, 'bad_request', refusals[why] ?? refusals['not-picture']);
  const type = sniff(raw);
  use(type ? { type, data: raw } : standIn);
  json(res, 200, { picture: { custom: true, version: state.version }, apps: await apply(req) });
});

on('DELETE', '/api/agent/picture', async (req, res) => {
  if (!adminOnly(req, res)) return;
  use(null);
  json(res, 200, { picture: { custom: false, version: state.version }, apps: await apply(req) });
});
