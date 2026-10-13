class Clock {
  now = $state(Date.now());
  #timer: ReturnType<typeof setInterval> | undefined;

  // A hidden tab skips the tick, so countdowns and relative times re-render
  // nothing in the background, and catches up the moment it is shown again.
  start() {
    if (this.#timer) return;
    const tick = () => {
      if (!document.hidden) this.now = Date.now();
    };
    this.#timer = setInterval(tick, 1000);
    document.addEventListener('visibilitychange', tick);
  }
}

// One shared one-second tick for countdowns and relative times.
export const clock = new Clock();
