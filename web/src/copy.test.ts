// The copy guide (docs/design/ui.md §14, §18.2 G6) checked in the source:
// template text, copy attributes and prose string literals in every .svelte
// and .ts file under src. The browser check (e2e/lib/copy.ts) reads rendered
// pages; this one also covers states no shot reaches.
import { parse } from 'svelte/compiler';
import { describe, expect, it } from 'vitest';
import { COPY_RULES, copyMatches, type CopyRule } from '../e2e/lib/copy-patterns';

// Every source file as text, keyed "./lib/format.ts".
const SOURCES = import.meta.glob<string>(['./**/*.svelte', './**/*.ts'], { query: '?raw', import: 'default', eager: true });

// §14.2 "Never use" and §14.1 voice, beyond the G6 patterns.
const VOICE: CopyRule[] = [
  { id: 'banned-phrase', pattern: /\b(oops|whoops|simply|easily|seamless(ly)?|magic(al)?|ai-powered|supercharge[sd]?|please)\b/i, why: 'a phrase the copy guide bans (§14.2)' },
  { id: 'error-prefix', pattern: /(^|[.:]\s)error:/i, why: '"error:" as a prefix (§14.4)' },
  { id: 'personal-voice', pattern: /\b(your machine|your own assistant|plan a calm week)\b/i, why: 'personal-assistant voice (§14.1)' },
  // Acronyms and file names (SKILL.md) are not shouting.
  { id: 'all-caps', pattern: /\b(?!(?:API|AWS|CLI|URL|MCP|SSO|IRSA|GPT|OSS|UTC|USD|YAML|JSON|HTTP|HTTPS|TLS|DNS|CPU|RAM|PAT|SHA|CSV|PDF|PNG|SVG|ZIP)\b)[A-Z]{3,}\b(?!\.[a-z])/, why: 'an all-caps word (§14.2)' },
  { id: 'emoji', pattern: /\p{Emoji_Presentation}/u, why: 'emoji (§14.2)' },
];
const RULES = [...COPY_RULES, ...VOICE];

// Where config keys, tool names and ids belong (§4.7, §14.3).
const allowed = (file: string) => file.startsWith('screens/settings/advanced/') || /(^|\/)[A-Za-z]*Details\.svelte$/.test(file) || file.endsWith('.test.ts') || /(^|\/)test\//.test(file);

interface Found {
  text: string;
  at: number;
}

type Node = { type: string; start: number; end: number; [k: string]: unknown };

// Attributes that never hold words people read.
const CODE_ATTRS = /^(class|id|href|src|for|name|type|role|value|form|rel|target|method|action|autocomplete|inputmode|enterkeyhint|accept|lang|dir|tabindex|style|slot|this|key|mode|variant|size|tone|side|placement|icon|initialFocus|width|height|viewBox|d|fill|stroke|data-.*|aria-(controls|describedby|labelledby|owns|activedescendant|current|haspopup|live|expanded|selected|checked|pressed|hidden|modal|busy|invalid|disabled|level|orientation|sort|valuenow|valuemin|valuemax|atomic|relevant))$/;

// Subtrees that are not main-flow copy: Details content, verbatim values and code.
function skipped(n: Node): boolean {
  const attrs = (n.attributes as Node[] | undefined) ?? [];
  const has = (name: string) => attrs.some((a) => a.type === 'Attribute' && a.name === name);
  if (has('data-details') || has('data-copy-ignore')) return true;
  if (n.type === 'RegularElement' && ['code', 'pre', 'script', 'style'].includes(n.name as string)) return true;
  if (n.type === 'Component' && ['CodeBlock', 'CommandBlock', 'DiffView', 'Verbatim', 'Markdown'].includes(n.name as string)) return true;
  if (n.type === 'Component' && n.name === 'Disclosure') {
    const v = attrs.find((a) => a.type === 'Attribute' && a.name === 'variant') as { value?: unknown } | undefined;
    const variant = Array.isArray(v?.value) ? (v.value as { data?: string }[]).map((x) => x.data ?? '').join('') : v ? '?' : 'details';
    return variant === 'details';
  }
  return false;
}

// A literal in script code is copy only when it reads as words: one
// capitalised word, or several words not all of which look like class names,
// keys, paths or identifiers.
const CLASSY = /^[!a-z0-9@:[\]()/._%&>~*=#'"+,|-]+$/;
const UTILITY = new Set('flex grid block inline hidden contents truncate italic underline relative absolute fixed sticky static grow shrink border rounded transition invisible visible table group peer isolate antialiased outline ring shadow collapse resize'.split(' '));
function prose(s: string): boolean {
  const t = s.replace(/\s+/g, ' ').trim();
  if (!/[A-Za-z]{2}/.test(t)) return false;
  if (/^(\/|\.\.?\/|https?:|mailto:|#|\[|@|[a-z]+:\/\/|<)/.test(t)) return false;
  // A PEM marker a key is checked against, never shown as words.
  if (/^[A-Z ]*PRIVATE KEY$/.test(t)) return false;
  const words = t.split(' ');
  if (words.length === 1) return /^[A-Z][a-z']+[.?…]?$/.test(t);
  return !words.every((w) => CLASSY.test(w) && (/[-:/[\]._]/.test(w) || UTILITY.has(w)));
}

function literals(root: unknown, out: Found[], offset = 0) {
  const walk = (n: unknown) => {
    if (!n || typeof n !== 'object') return;
    if (Array.isArray(n)) return n.forEach(walk);
    const node = n as Node;
    if (node.type === 'Literal' && typeof node.value === 'string') {
      if (prose(node.value)) out.push({ text: node.value, at: node.start + offset });
      return;
    }
    if (node.type === 'TemplateLiteral') {
      const text = (node.quasis as { value: { cooked: string } }[]).map((q) => q.value.cooked ?? '').join('…');
      if (prose(text)) out.push({ text, at: node.start + offset });
      (node.expressions as unknown[]).forEach(walk);
      return;
    }
    // Types, imports, re-exports and object keys are code, never copy.
    if (/^TS(?!(As|Satisfies|NonNull)Expression$)|^Import|^ExportAllDeclaration$/.test(node.type)) return;
    if (node.type === 'ExportNamedDeclaration' && !node.declaration) return;
    for (const [k, v] of Object.entries(node)) {
      if (k === 'type' || k === 'start' || k === 'end' || k === 'loc' || k === 'range') continue;
      if (k === 'key' && (node.type === 'Property' || node.type === 'MethodDefinition' || node.type === 'PropertyDefinition') && !node.computed) continue;
      walk(v);
    }
  };
  walk(root);
}

function template(n: unknown, out: Found[]) {
  if (!n || typeof n !== 'object') return;
  if (Array.isArray(n)) return n.forEach((x) => template(x, out));
  const node = n as Node;
  if (skipped(node)) return;
  switch (node.type) {
    case 'Text':
      if ((node.data as string).trim()) out.push({ text: node.data as string, at: node.start });
      return;
    case 'ExpressionTag':
      literals(node.expression, out);
      return;
    case 'Attribute': {
      const name = node.name as string;
      if (CODE_ATTRS.test(name)) return;
      const v = node.value;
      if (Array.isArray(v)) for (const part of v as Node[]) part.type === 'Text' ? (part.data as string).trim() && out.push({ text: part.data as string, at: part.start }) : literals(part.expression, out);
      else if (v && typeof v === 'object') literals((v as Node).expression, out);
      return;
    }
  }
  for (const [k, v] of Object.entries(node)) {
    if (k === 'type' || k === 'start' || k === 'end' || k === 'metadata') continue;
    template(v, out);
  }
}

function strings(file: string, src: string): Found[] {
  const out: Found[] = [];
  if (file.endsWith('.svelte')) {
    const ast = parse(src, { modern: true }) as unknown as { fragment: unknown; instance?: { content: unknown }; module?: { content: unknown } };
    literals(ast.instance?.content, out);
    literals(ast.module?.content, out);
    template(ast.fragment, out);
  } else {
    // The Svelte parser reads TypeScript inside a script tag; positions shift by the tag.
    const open = '<script lang="ts">';
    const ast = parse(`${open}${src}</script>`, { modern: true }) as unknown as { instance?: { content: unknown } };
    literals(ast.instance?.content, out, -open.length);
  }
  return out;
}

const lineOf = (src: string, at: number) => src.slice(0, Math.max(0, at)).split('\n').length;

describe('interface copy in the source (§14)', () => {
  const files = Object.keys(SOURCES)
    .map((p) => p.slice(2))
    .filter((f) => !allowed(f) && f !== 'lib/api/types.gen.ts');
  const found = files.flatMap((f) => {
    const src = SOURCES[`./${f}`];
    return strings(f, src).map((s) => ({ ...s, where: `${f}:${lineOf(src, s.at)}` }));
  });

  it('reads template text, copy attributes and prose literals', () => {
    expect(strings('x.svelte', '<script>const a = "flex gap-2"; const b = "Saved.";</script><p title="Saved." class="mt-2">Delete {name}?</p>').map((f) => f.text)).toEqual([
      'Saved.',
      'Saved.',
      'Delete ',
      '?',
    ]);
    expect(files.length).toBeGreaterThan(200);
    expect(found.length).toBeGreaterThan(3000);
  });

  it('keeps §14.3 words, exclamation marks, ids and banned phrases out of the main flow', () => {
    const problems: string[] = [];
    for (const s of found) {
      // PEM armour ("-----BEGIN RSA PRIVATE KEY-----") shows the format a field expects, verbatim.
      for (const m of copyMatches(s.text.replace(/-----(BEGIN|END) [A-Z ]+-----/g, ''), RULES)) problems.push(`${s.where} ${m.rule} "${m.match}" in ${JSON.stringify(s.text.replace(/\s+/g, ' ').trim().slice(0, 100))}`);
    }
    expect(problems).toEqual([]);
  });
});
