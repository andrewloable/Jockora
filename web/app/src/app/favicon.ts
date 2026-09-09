/**
 * Which mark the browser tab shows.
 *
 * TWO TABS, ONE PRODUCT. An operator runs the console and the station side by
 * side -- that is the normal way to work, not an edge case -- and two tabs
 * carrying the same icon means clicking the wrong one. The two marks share a
 * pointer and differ in silhouette, round against square, because a colour
 * change alone is not tellable at the 16px a tab strip actually draws.
 *
 * SET FROM THE ROUTE ROOTS rather than from the router. Each of the two top
 * level components already knows which half of the product it is; watching the
 * URL to rediscover that would be a second source of truth for a fact the
 * component was constructed to represent.
 */
export const LISTENER_ICON = 'icon.svg';
export const ADMIN_ICON = 'icon-admin.svg';

/**
 * Point the tab at one of the two marks.
 *
 * THE LINK IS IN index.html and is never absent in the app, but a missing one
 * is not worth a crash on a route change: the tab keeps whatever it had, which
 * is the harmless outcome. jsdom in a component test that renders no document
 * head is the other way in.
 */
export function setFavicon(href: string): void {
  const link = document.querySelector<HTMLLinkElement>('link[rel="icon"]');
  if (link) {
    link.href = href;
  }
}
