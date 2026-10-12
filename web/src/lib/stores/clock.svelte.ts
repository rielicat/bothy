class Clock {
  now = $state(Date.now());
  #timer: ReturnType<typeof setInterval> | undefined;

  start() {
    this.#timer ??= setInterval(() => (this.now = Date.now()), 1000);
  }
}

// One shared one-second tick for countdowns and relative times.
export const clock = new Clock();
