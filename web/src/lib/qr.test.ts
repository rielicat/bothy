import { describe, expect, it } from 'vitest';
import { codewords, formatBits, MASKS, qr, rsEncode } from './qr';

// Reads a symbol back: format bits → mask, unmask, zigzag codewords,
// de-interleave and parse the byte-mode segment. It shares no code with the
// encoder's placement, so a round trip checks layout, masking and order.
function decode(g: boolean[][]): string {
  const size = g.length;
  const version = (size - 17) / 4;
  let fmt = 0;
  for (let i = 0; i <= 5; i++) fmt |= Number(g[i][8]) << i;
  fmt |= Number(g[7][8]) << 6;
  fmt |= Number(g[8][8]) << 7;
  fmt |= Number(g[8][7]) << 8;
  for (let i = 9; i < 15; i++) fmt |= Number(g[8][14 - i]) << i;
  const mask = [0, 1, 2, 3, 4, 5, 6, 7].find((k) => formatBits(k) === fmt);
  if (mask === undefined) throw new Error('bad format bits');
  const fn = (x: number, y: number) => {
    if (x === 6 || y === 6) return true;
    if ((x < 9 && y < 9) || (x >= size - 8 && y < 9) || (x < 9 && y >= size - 8)) return true;
    if (version > 1 && Math.abs(x - (size - 7)) <= 2 && Math.abs(y - (size - 7)) <= 2) return true;
    return false;
  };
  const bits: number[] = [];
  for (let right = size - 1; right >= 1; right -= 2) {
    if (right === 6) right = 5;
    for (let vert = 0; vert < size; vert++)
      for (let j = 0; j < 2; j++) {
        const x = right - j;
        const y = ((right + 1) & 2) === 0 ? size - 1 - vert : vert;
        if (!fn(x, y)) bits.push(Number(g[y][x] !== MASKS[mask](x, y)));
      }
  }
  const words: number[] = [];
  for (let i = 0; i + 8 <= bits.length; i += 8) words.push(bits.slice(i, i + 8).reduce((a, b) => (a << 1) | b, 0));
  const blocks = [1, 1, 1, 2, 2, 4][version - 1];
  const per = [16, 28, 44, 32, 43, 27][version - 1];
  const data: number[] = [];
  for (let b = 0; b < blocks; b++) for (let i = 0; i < per; i++) data.push(words[i * blocks + b]);
  expect(data[0] >> 4).toBe(0b0100);
  const len = ((data[0] & 0xf) << 4) | (data[1] >> 4);
  const out: number[] = [];
  for (let i = 0; i < len; i++) out.push(((data[1 + i] & 0xf) << 4) | (data[2 + i] >> 4));
  return new TextDecoder().decode(new Uint8Array(out));
}

describe('qr', () => {
  it('computes Reed–Solomon codewords (ISO 18004 / thonky "HELLO WORLD" 1-M)', () => {
    const data = [32, 91, 11, 120, 209, 114, 220, 77, 67, 64, 236, 17, 236, 17, 236, 17];
    expect(rsEncode(data, 10)).toEqual([196, 35, 39, 119, 235, 215, 231, 226, 93, 23]);
  });

  it('produces the standard level-M format strings', () => {
    const table = ['101010000010010', '101000100100101', '101111001111100', '101101101001011', '100010111111001', '100000011001110', '100111110010111', '100101010100000'];
    expect([0, 1, 2, 3, 4, 5, 6, 7].map((k) => formatBits(k).toString(2).padStart(15, '0'))).toEqual(table);
  });

  it('picks the smallest version that fits and pads with 0xEC/0x11', () => {
    expect(codewords('hi').version).toBe(1);
    expect(codewords('x'.repeat(14)).version).toBe(1);
    expect(codewords('x'.repeat(15)).version).toBe(2);
    expect(codewords('x'.repeat(106)).version).toBe(6);
    expect(() => codewords('x'.repeat(107))).toThrow(RangeError);
    // mode 0100, length 2, "hi", terminator: exactly four bytes, then padding
    const { words } = codewords('hi');
    expect(words.slice(0, 7)).toEqual([0x40, 0x26, 0x86, 0x90, 0xec, 0x11, 0xec]);
  });

  it('draws finder, timing and dark-module patterns', () => {
    const g = qr('https://t.me/sams_bothy_bot?start=7KQ2MX4D');
    const size = g.length;
    expect(size).toBe(29); // 42 bytes: version 3
    for (const [cx, cy] of [
      [3, 3],
      [size - 4, 3],
      [3, size - 4],
    ]) {
      expect(g[cy][cx]).toBe(true);
      expect(g[cy - 2][cx]).toBe(false);
      expect(g[cy - 3][cx]).toBe(true);
    }
    for (let i = 8; i < size - 8; i++) expect(g[6][i]).toBe(i % 2 === 0);
    expect(g[size - 8][8]).toBe(true);
  });

  it('round-trips text of every supported version, including UTF-8', () => {
    for (const text of ['A', 'https://t.me/bot?start=7KQ2MX4D', 'bothy pairing ✓ ünïcode', 'x'.repeat(60), 'y'.repeat(84), 'z'.repeat(106)]) {
      expect(decode(qr(text))).toBe(text);
    }
  });
});
