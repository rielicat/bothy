// jsdom lacks a few browser APIs the app touches; these stand-ins keep
// component tests on the real code paths.
import { afterEach } from 'vitest';
import { cleanup } from '@testing-library/svelte';

const dialog = HTMLDialogElement.prototype;
if (!('showModal' in dialog) || typeof dialog.showModal !== 'function') {
  dialog.showModal = function (this: HTMLDialogElement) {
    this.setAttribute('open', '');
  };
  dialog.close = function (this: HTMLDialogElement) {
    if (!this.hasAttribute('open')) return;
    this.removeAttribute('open');
    this.dispatchEvent(new Event('close'));
  };
}
Element.prototype.scrollIntoView ??= function () {};
window.scrollTo = () => {};
// Node's own createObjectURL rejects jsdom's Blob, so both are replaced.
URL.createObjectURL = () => 'blob:test';
URL.revokeObjectURL = () => {};

export class FakeEventSource {
  static instances: FakeEventSource[] = [];
  static readonly CONNECTING = 0;
  static readonly OPEN = 1;
  static readonly CLOSED = 2;
  readyState = FakeEventSource.CONNECTING;
  onopen: (() => void) | null = null;
  onerror: (() => void) | null = null;
  #listeners = new Map<string, ((m: MessageEvent) => void)[]>();

  constructor(readonly url: string) {
    FakeEventSource.instances.push(this);
  }

  addEventListener(type: string, fn: (m: MessageEvent) => void) {
    this.#listeners.set(type, [...(this.#listeners.get(type) ?? []), fn]);
  }

  close() {
    this.readyState = FakeEventSource.CLOSED;
  }

  open() {
    this.readyState = FakeEventSource.OPEN;
    this.onopen?.();
  }

  emit(type: string, data: unknown) {
    const m = new MessageEvent(type, { data: typeof data === 'string' ? data : JSON.stringify(data) });
    this.#listeners.get(type)?.forEach((fn) => fn(m));
  }

  fail(closed: boolean) {
    this.readyState = closed ? FakeEventSource.CLOSED : FakeEventSource.CONNECTING;
    this.onerror?.();
  }

  static get last(): FakeEventSource {
    return FakeEventSource.instances[FakeEventSource.instances.length - 1];
  }
}

(globalThis as unknown as { EventSource: unknown }).EventSource = FakeEventSource;

afterEach(() => cleanup());
