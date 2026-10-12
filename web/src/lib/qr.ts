// QR Code encoder (ISO/IEC 18004): byte mode, versions 1–6, error correction
// level M, which fits pairing deep links (≤ 106 bytes). Loaded lazily.

// [data codewords per block, blocks, EC codewords per block] for M, by version.
const BLOCKS: [number, number, number][] = [
  [16, 1, 10],
  [28, 1, 16],
  [44, 1, 26],
  [32, 2, 18],
  [43, 2, 24],
  [27, 4, 16],
];

export function gfMul(x: number, y: number): number {
  let z = 0;
  for (let i = 7; i >= 0; i--) {
    z = (z << 1) ^ ((z >>> 7) * 0x11d);
    z ^= ((y >>> i) & 1) * x;
  }
  return z & 0xff;
}

// Reed–Solomon EC codewords for one block (generator roots α^0 … α^(n-1)).
export function rsEncode(data: ArrayLike<number>, n: number): number[] {
  const gen = new Array<number>(n).fill(0);
  gen[n - 1] = 1;
  let root = 1;
  for (let i = 0; i < n; i++) {
    for (let j = 0; j < n; j++) {
      gen[j] = gfMul(gen[j], root);
      if (j + 1 < n) gen[j] ^= gen[j + 1];
    }
    root = gfMul(root, 2);
  }
  const rem = new Array<number>(n).fill(0);
  for (let k = 0; k < data.length; k++) {
    const f = data[k] ^ rem.shift()!;
    rem.push(0);
    for (let i = 0; i < n; i++) rem[i] ^= gfMul(gen[i], f);
  }
  return rem;
}

// 15-bit format information for level M (indicator 00) and the given mask.
export function formatBits(mask: number): number {
  const data = mask; // (0b00 << 3) | mask
  let rem = data;
  for (let i = 0; i < 10; i++) rem = (rem << 1) ^ ((rem >>> 9) * 0x537);
  return ((data << 10) | rem) ^ 0x5412;
}

export function codewords(text: string): { version: number; words: number[] } {
  const bytes = new TextEncoder().encode(text);
  const version = BLOCKS.findIndex(([d, b]) => d * b * 8 >= 12 + bytes.length * 8) + 1;
  if (version === 0) throw new RangeError('QR: text too long for version 6-M');
  const [dPer, blocks, ecPer] = BLOCKS[version - 1];
  const cap = dPer * blocks;
  const bits: number[] = [];
  const put = (v: number, len: number) => {
    for (let i = len - 1; i >= 0; i--) bits.push((v >>> i) & 1);
  };
  put(0b0100, 4);
  put(bytes.length, 8);
  for (const b of bytes) put(b, 8);
  put(0, Math.min(4, cap * 8 - bits.length));
  put(0, (8 - (bits.length % 8)) % 8);
  const data: number[] = [];
  for (let i = 0; i < bits.length; i += 8) data.push(bits.slice(i, i + 8).reduce((a, b) => (a << 1) | b, 0));
  for (let pad = 0xec; data.length < cap; pad ^= 0xec ^ 0x11) data.push(pad);
  const split = Array.from({ length: blocks }, (_, i) => data.slice(i * dPer, (i + 1) * dPer));
  const ec = split.map((b) => rsEncode(b, ecPer));
  const words: number[] = [];
  for (let i = 0; i < dPer; i++) for (const b of split) words.push(b[i]);
  for (let i = 0; i < ecPer; i++) for (const e of ec) words.push(e[i]);
  return { version, words };
}

export const MASKS: ((x: number, y: number) => boolean)[] = [
  (x, y) => (x + y) % 2 === 0,
  (_, y) => y % 2 === 0,
  (x) => x % 3 === 0,
  (x, y) => (x + y) % 3 === 0,
  (x, y) => (Math.floor(x / 3) + Math.floor(y / 2)) % 2 === 0,
  (x, y) => ((x * y) % 2) + ((x * y) % 3) === 0,
  (x, y) => (((x * y) % 2) + ((x * y) % 3)) % 2 === 0,
  (x, y) => (((x + y) % 2) + ((x * y) % 3)) % 2 === 0,
];

// Returns rows of booleans (true = dark), without the quiet zone.
export function qr(text: string): boolean[][] {
  const { version, words } = codewords(text);
  const size = 17 + 4 * version;
  const m = Array.from({ length: size }, () => new Array<boolean>(size).fill(false));
  const fn = Array.from({ length: size }, () => new Array<boolean>(size).fill(false));
  const set = (x: number, y: number, dark: boolean) => {
    m[y][x] = dark;
    fn[y][x] = true;
  };

  for (let i = 0; i < size; i++) {
    set(6, i, i % 2 === 0);
    set(i, 6, i % 2 === 0);
  }
  for (const [cx, cy] of [
    [3, 3],
    [size - 4, 3],
    [3, size - 4],
  ]) {
    for (let dy = -4; dy <= 4; dy++)
      for (let dx = -4; dx <= 4; dx++) {
        const x = cx + dx;
        const y = cy + dy;
        const d = Math.max(Math.abs(dx), Math.abs(dy));
        if (x >= 0 && x < size && y >= 0 && y < size) set(x, y, d !== 2 && d !== 4);
      }
  }
  if (version > 1) {
    const p = size - 7;
    for (let dy = -2; dy <= 2; dy++) for (let dx = -2; dx <= 2; dx++) set(p + dx, p + dy, Math.max(Math.abs(dx), Math.abs(dy)) !== 1);
  }
  // Format information (two copies) and the always-dark module; reserved
  // before data placement, written per mask candidate.
  const writeFormat = (g: boolean[][], bits: number) => {
    const put = (x: number, y: number, dark: boolean) => {
      g[y][x] = dark;
      fn[y][x] = true;
    };
    const bit = (i: number) => ((bits >>> i) & 1) === 1;
    for (let i = 0; i <= 5; i++) put(8, i, bit(i));
    put(8, 7, bit(6));
    put(8, 8, bit(7));
    put(7, 8, bit(8));
    for (let i = 9; i < 15; i++) put(14 - i, 8, bit(i));
    for (let i = 0; i < 8; i++) put(size - 1 - i, 8, bit(i));
    for (let i = 8; i < 15; i++) put(8, size - 15 + i, bit(i));
    put(8, size - 8, true);
  };
  writeFormat(m, 0);

  let i = 0;
  for (let right = size - 1; right >= 1; right -= 2) {
    if (right === 6) right = 5;
    for (let vert = 0; vert < size; vert++) {
      for (let j = 0; j < 2; j++) {
        const x = right - j;
        const y = ((right + 1) & 2) === 0 ? size - 1 - vert : vert;
        if (!fn[y][x] && i < words.length * 8) {
          m[y][x] = ((words[i >>> 3] >>> (7 - (i & 7))) & 1) === 1;
          i++;
        }
      }
    }
  }

  let best = m;
  let bestScore = Infinity;
  for (let k = 0; k < 8; k++) {
    const cand = m.map((row, y) => row.map((dark, x) => (fn[y][x] ? dark : dark !== MASKS[k](x, y))));
    writeFormat(cand, formatBits(k));
    const score = penalty(cand);
    if (score < bestScore) {
      bestScore = score;
      best = cand;
    }
  }
  return best;
}

// Penalty rules N1–N4 of the standard, used to pick the mask.
export function penalty(g: boolean[][]): number {
  const n = g.length;
  let score = 0;
  let dark = 0;
  const lines = (get: (a: number, b: number) => boolean) => {
    for (let a = 0; a < n; a++) {
      let run = 1;
      let s = '';
      for (let b = 0; b < n; b++) {
        const v = get(a, b);
        s += v ? '1' : '0';
        if (b > 0 && v === get(a, b - 1)) {
          run++;
          if (run === 5) score += 3;
          else if (run > 5) score++;
        } else run = 1;
      }
      for (const pat of ['10111010000', '00001011101']) for (let k = s.indexOf(pat); k >= 0; k = s.indexOf(pat, k + 1)) score += 40;
    }
  };
  lines((a, b) => g[a][b]);
  lines((a, b) => g[b][a]);
  for (let y = 0; y < n; y++)
    for (let x = 0; x < n; x++) {
      if (g[y][x]) dark++;
      if (x + 1 < n && y + 1 < n && g[y][x] === g[y][x + 1] && g[y][x] === g[y + 1][x] && g[y][x] === g[y + 1][x + 1]) score += 3;
    }
  score += Math.floor(Math.abs((dark * 100) / (n * n) - 50) / 5) * 10;
  return score;
}
