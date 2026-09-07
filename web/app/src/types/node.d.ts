// The two node builtins the structural specs use to read the source tree off
// disk. Declared here rather than by adding @types/node: this is the whole of
// what is used, and a dependency added for three signatures is a dependency
// the licence gate has to account for forever.

declare module 'node:fs' {
  export interface Dirent {
    name: string;
    isDirectory(): boolean;
  }
  export function readdirSync(path: string, options: { withFileTypes: true }): Dirent[];
  export function readFileSync(path: string, encoding: 'utf8'): string;
}

declare module 'node:path' {
  export function join(...parts: string[]): string;
}

interface ImportMeta {
  /** The directory of this module. Node sets it; the browser does not. */
  readonly dirname: string;
}
