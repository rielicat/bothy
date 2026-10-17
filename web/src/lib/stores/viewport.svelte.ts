// Phone width (under 640 px), for the few controls that move to another
// place on a phone rather than restyle there, so only one copy is ever in
// the page. Without matchMedia (tests) it reads as wide.
class Narrow {
  current = $state(false);

  constructor() {
    if (typeof window === 'undefined' || typeof window.matchMedia !== 'function') return;
    const mq = window.matchMedia('(max-width: 639px)');
    this.current = mq.matches;
    mq.addEventListener?.('change', (e) => (this.current = e.matches));
  }
}

export const narrow = new Narrow();
