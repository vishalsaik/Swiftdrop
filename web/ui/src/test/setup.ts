import { afterEach } from 'vitest';
import { cleanup, configure } from '@testing-library/react';

afterEach(() => cleanup());

// The default is one second to find something on screen, which a busy machine (a database and
// Docker running beside the tests, or other projects' clusters) can miss while the app is still
// starting: with five seconds the orb test and a few others still timed out, with the machine
// doing other work. A test that waits for something that never comes still fails, just later.
configure({ asyncUtilTimeout: 10_000 });

// jsdom does not draw modal dialogs; give it the two methods the sheets use.
if (typeof HTMLDialogElement !== 'undefined') {
  const proto = HTMLDialogElement.prototype as HTMLDialogElement & { showModal?: () => void };
  if (!proto.showModal) {
    proto.showModal = function showModal(this: HTMLDialogElement) { this.setAttribute('open', ''); };
    proto.close = function close(this: HTMLDialogElement) {
      if (!this.hasAttribute('open')) return;
      this.removeAttribute('open');
      this.dispatchEvent(new Event('close'));
    };
  }
}

// jsdom has neither of these; the effect libraries ask for them. A stub that never matches
// and never reports anything on screen is enough, and a test that needs more stubs its own.
if (typeof window.matchMedia !== 'function') {
  window.matchMedia = ((query: string) => ({
    matches: false, media: query, onchange: null,
    addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false,
  })) as unknown as typeof window.matchMedia;
}
if (typeof globalThis.IntersectionObserver === 'undefined') {
  globalThis.IntersectionObserver = class {
    observe() {}
    unobserve() {}
    disconnect() {}
    takeRecords() { return []; }
    root = null; rootMargin = ''; thresholds = [];
  } as unknown as typeof IntersectionObserver;
}
