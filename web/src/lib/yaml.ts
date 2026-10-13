import type { Entry } from './api/types.gen';
import type { SchemaNode } from './config.svelte';

interface Node {
  entry?: Entry;
  schema?: SchemaNode;
  kids: Map<string, Node>;
}

const itemSchema = (s?: SchemaNode): SchemaNode | undefined =>
  s?.items?.properties ? s.items : typeof s?.additionalProperties === 'object' && s.additionalProperties.properties ? s.additionalProperties : undefined;

// Rebuilds YAML from effective-config leaves. Under list-of-object and map
// nodes the item key is everything up to the first known field name, so ids
// that contain dots (model refs in pricing) stay whole.
export function toYaml(entries: Entry[], root: SchemaNode | undefined, comment?: (e: Entry) => string): string {
  const top: Node = { schema: root, kids: new Map() };
  for (const e of entries) {
    const segs = e.path.split('.');
    let node = top;
    for (let i = 0; i < segs.length; i++) {
      const item = itemSchema(node.schema);
      let key = segs[i];
      if (item) {
        const k = segs.findIndex((s, j) => j > i && item.properties![s]);
        if (k > i) {
          key = segs.slice(i, k).join('.');
          i = k - 1;
        }
      }
      let next = node.kids.get(key);
      if (!next) {
        next = { kids: new Map(), schema: item ?? node.schema?.properties?.[key] };
        node.kids.set(key, next);
      }
      node = next;
    }
    node.entry = e;
  }
  const lines: string[] = [];
  const keyText = (k: string) => (/^[A-Za-z0-9_][A-Za-z0-9_.\/-]*$/.test(k) ? k : JSON.stringify(k));
  const walk = (node: Node, indent: string) => {
    const list = !!node.schema?.items?.properties;
    for (const [k, child] of node.kids) {
      const note = child.entry && comment ? `  # ${comment(child.entry)}` : '';
      if (list) {
        lines.push(`${indent}- # ${k}`);
        walk(child, indent + '  ');
      } else if (child.entry && !child.kids.size) {
        lines.push(`${indent}${keyText(k)}: ${JSON.stringify(child.entry.value ?? null)}${note}`);
      } else {
        lines.push(`${indent}${keyText(k)}:`);
        walk(child, indent + '  ');
      }
    }
  };
  walk(top, '');
  return lines.join('\n') + '\n';
}
