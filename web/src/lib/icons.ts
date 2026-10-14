// Hand-drawn 24×24 stroke icons (one path each, round caps and joins): the
// 18 of docs/design/ui.md §3.7 and nothing else.
const c = (cx: number, cy: number, r: number) => `M${cx - r} ${cy}a${r} ${r} 0 1 0 ${2 * r} 0a${r} ${r} 0 1 0 ${-2 * r} 0z`;

export const paths = {
  plus: 'M12 5v14M5 12h14',
  search: c(11, 11, 6.5) + 'M20 20l-4.3-4.3',
  'chevron-down': 'M6 9l6 6 6-6',
  'chevron-right': 'M9 6l6 6-6 6',
  more: c(5, 12, 0.75) + c(12, 12, 0.75) + c(19, 12, 0.75),
  close: 'M6 6l12 12M18 6L6 18',
  check: 'M5 12.5l4.5 4.5L19 7.5',
  alert: 'M12 3.5l9.5 16.5h-19zM12 10v4.5M12 17.4v.1',
  clock: c(12, 12, 9) + 'M12 7v5l3 2',
  'arrow-up': 'M12 19V5M6 11l6-6 6 6',
  stop: 'M7.5 7.5h9v9h-9z',
  paperclip: 'M20 11.5l-8.2 8.2a5 5 0 0 1-7-7l8.6-8.6a3.4 3.4 0 0 1 4.8 4.8l-8.6 8.6a1.8 1.8 0 0 1-2.6-2.6l7.9-7.9',
  copy: 'M9 9h10v10H9zM5 15V5h10',
  external: 'M14 4h6v6M20 4l-9 9M18 14v5H5V6h5',
  menu: 'M4 7h16M4 12h16M4 17h16',
  back: 'M19 12H5M11 6l-6 6 6 6',
  refresh: 'M20 11a8 8 0 1 0-2.3 5.7M20 4v7h-7',
  spinner: 'M12 3a9 9 0 1 1-9 9',
} as const;

export type IconName = keyof typeof paths;
