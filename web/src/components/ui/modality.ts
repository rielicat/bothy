// Whether the person's last input was a key press rather than a pointer, the
// way browsers decide :focus-visible. Text fields match :focus-visible however
// they got focus, so a field that takes focus by itself (the composer) asks
// this to keep its full focus ring for keyboard users only (§3.5).
let keys = false;

if (typeof window !== 'undefined') {
  window.addEventListener(
    'keydown',
    (e) => {
      if (!['Shift', 'Control', 'Alt', 'Meta'].includes(e.key)) keys = true;
    },
    true,
  );
  window.addEventListener('pointerdown', () => (keys = false), true);
}

export const fromKeyboard = () => keys;
