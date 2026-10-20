// One toast at a time (§15.3 row 20): a new one replaces the current one.
// Toasts confirm what just happened ("Saved.", "Chat deleted."); errors never
// go here, they stay next to what caused them (§14.4).

export interface ToastAction {
  label: string;
  onclick: () => void;
}

export interface ToastItem {
  id: number;
  text: string;
  action?: ToastAction;
  duration: number;
}

class Toaster {
  current: ToastItem | null = $state(null);
  /** Extra distance from the bottom edge, set by whatever sits there on phones (the composer). */
  offset = $state(0);

  /** Mounted hosts; only the first renders, so a page that mounts its own host never shows a toast twice. */
  hosts: symbol[] = $state([]);

  #id = 0;
  #timer: ReturnType<typeof setTimeout> | undefined;
  #remaining = 0;
  #started = 0;
  #paused = false;

  show(text: string, opts: { action?: ToastAction; duration?: number } = {}): number {
    const id = ++this.#id;
    // An action needs time to be read and reached, more than a plain "Saved." does.
    this.current = { id, text, action: opts.action, duration: opts.duration ?? (opts.action ? 8000 : 4000) };
    this.#paused = false;
    this.#run(this.current.duration);
    return id;
  }

  dismiss(id?: number) {
    if (id !== undefined && this.current?.id !== id) return;
    clearTimeout(this.#timer);
    this.current = null;
  }

  pause() {
    if (!this.current || this.#paused) return;
    this.#paused = true;
    clearTimeout(this.#timer);
    this.#remaining -= Date.now() - this.#started;
  }

  resume() {
    if (!this.current || !this.#paused) return;
    this.#paused = false;
    this.#run(Math.max(this.#remaining, 1000));
  }

  #run(ms: number) {
    clearTimeout(this.#timer);
    this.#remaining = ms;
    this.#started = Date.now();
    const id = this.current?.id;
    this.#timer = setTimeout(() => this.dismiss(id), ms);
  }
}

export const toaster = new Toaster();

/** Shows a short past-tense confirmation, five words or fewer: toast('Saved.'). */
export function toast(text: string, opts: { action?: ToastAction; duration?: number } = {}): number {
  return toaster.show(text, opts);
}
