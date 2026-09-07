/**
 * The light build ships no types of its own; its API is the full build's.
 *
 * We import the light bundle because the full one carries subtitle, alt-audio
 * and EME support this product has no use for, at 300kB of a listener's first
 * load on a phone.
 */
declare module 'hls.js/dist/hls.light.min.mjs' {
  import Hls from 'hls.js';
  export default Hls;
  export * from 'hls.js';
}
