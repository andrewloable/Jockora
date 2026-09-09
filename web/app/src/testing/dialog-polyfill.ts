// Copyright (C) 2026 Andrew Loable
// SPDX-License-Identifier: AGPL-3.0-only

/**
 * The dialog behaviour jsdom does not have.
 *
 * WHY THIS EXISTS RATHER THAN A FEATURE CHECK IN THE COMPONENT. jsdom
 * implements neither showModal nor close -- both read undefined and calling
 * showModal throws -- while every browser this product targets has had them for
 * years. Feature-detecting in production code to satisfy the test environment
 * would put a branch in the app that no user can ever take, and it would have
 * to be covered by a test asserting the fallback nobody runs.
 *
 * So the environment gets the platform, and the component stays the code that
 * actually ships.
 *
 * IT MIRRORS THE REAL SEMANTICS, not just the signatures: showModal sets open,
 * close clears it and fires a close event, and Escape fires cancel first so a
 * dialog can refuse to go. Anything looser would let a test pass against a
 * component that does not work in a browser.
 */
export function installDialogPolyfill(): void {
  const proto = HTMLDialogElement.prototype as HTMLDialogElement & {
    showModal?: () => void;
    close?: (returnValue?: string) => void;
  };
  if (typeof proto.showModal === 'function') {
    return;
  }

  proto.showModal = function showModal(this: HTMLDialogElement) {
    this.setAttribute('open', '');
    // The real thing moves focus to the first focusable child; the autofocus
    // element wins if there is one.
    const target =
      this.querySelector<HTMLElement>('[autofocus]') ??
      this.querySelector<HTMLElement>('input, select, textarea, button');
    target?.focus();
  };

  proto.close = function close(this: HTMLDialogElement, returnValue?: string) {
    if (!this.hasAttribute('open')) {
      return;
    }
    this.removeAttribute('open');
    if (returnValue !== undefined) {
      this.returnValue = returnValue;
    }
    this.dispatchEvent(new Event('close'));
  };

  // Escape asks first, exactly as a browser does: a cancel nobody prevents
  // closes the dialog, and preventDefault keeps it open.
  document.addEventListener('keydown', (e) => {
    if (e.key !== 'Escape') {
      return;
    }
    const open = document.querySelector<HTMLDialogElement>('dialog[open]');
    if (!open) {
      return;
    }
    const cancel = new Event('cancel', { cancelable: true });
    if (open.dispatchEvent(cancel)) {
      open.close();
    }
  });
}
