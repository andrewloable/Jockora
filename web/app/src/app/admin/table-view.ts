import { Signal, computed, signal } from '@angular/core';

/** One sortable, searchable column: how to get its value out of a row. */
export interface Column<T> {
  /** What a header passes to toggle(), and what sortKey holds. */
  key: string;
  /** The value to compare and to search. Blank and null are "nothing here". */
  value: (row: T) => string | number | null | undefined;
}

/**
 * Which column a table is sorted by, and which way round.
 *
 * SPLIT OUT OF TableView because the PLAYLIST cannot use TableView: it holds
 * fifty rows out of seven thousand and is ordered by the SERVER, so it needs
 * the header behaviour -- the toggle, the arrow, the aria-sort -- and none of
 * the filtering. A second copy of "a third press does not clear it" is how the
 * playlist headers and the other five come to behave differently.
 */
export class SortState {
  /** Which column is sorted, or null for the order the server sent. */
  readonly sortKey = signal<string | null>(null);
  readonly ascending = signal(true);

  /**
   * Sort by this column, or turn it round if it is already the sorted one.
   *
   * A THIRD PRESS DOES NOT CLEAR IT. Two states are what a person expects from
   * a header, and an unsorted state reachable by clicking twice is one nobody
   * finds on purpose and everybody hits by accident.
   */
  toggle(key: string): void {
    if (this.sortKey() === key) {
      this.ascending.update((up) => !up);
      return;
    }
    this.sortKey.set(key);
    this.ascending.set(true);
  }

  /**
   * What a screen reader is told about this header.
   *
   * The actual aria-sort values, not a made-up one: a column that is not sorted
   * says "none" rather than nothing, so the header is announced as sortable at
   * all.
   */
  ariaSort(key: string): 'ascending' | 'descending' | 'none' {
    if (this.sortKey() !== key) {
      return 'none';
    }
    return this.ascending() ? 'ascending' : 'descending';
  }

  /** The arrow drawn in the header, and nothing when the column is not sorted. */
  marker(key: string): string {
    if (this.sortKey() !== key) {
      return '';
    }
    return this.ascending() ? '▲' : '▼';
  }
}

/**
 * The search box and the click-to-sort headers, once rather than five times.
 *
 * WHY A SHARED THING AND NOT A COMPONENT. Every admin table draws its own cells
 * -- stations puts a marker under a name, ads render an aired timestamp, people
 * swap a row for edit boxes -- and a generic table component would have to grow
 * a slot for each of those. This owns only the three pieces of state that are
 * genuinely the same everywhere, and the tables keep their markup.
 *
 * ONLY FOR TABLES THAT HOLD EVERYTHING. The playlist is paged on the server, so
 * filtering there would narrow the fifty rows on screen out of seven thousand
 * and read as an answer about the whole library. Jockora-9g7 leaves it out on
 * purpose, and Jockora-22s gave the playlist the part that CAN be right across
 * a paged listing -- the sort, done by the server -- through SortState above.
 */
export class TableView<T> extends SortState {
  /** What the operator typed. Matched against every column's value. */
  readonly query = signal('');

  private readonly columns: ReadonlyMap<string, Column<T>>;

  constructor(
    private readonly rows: Signal<readonly T[]>,
    columns: readonly Column<T>[],
  ) {
    super();
    this.columns = new Map(columns.map((c) => [c.key, c]));
  }

  /** The rows to draw: searched, then sorted. */
  readonly shown = computed<T[]>(() => {
    const q = this.query().trim().toLowerCase();
    const found = q
      ? this.rows().filter((row) =>
          [...this.columns.values()].some((c) => text(c.value(row)).toLowerCase().includes(q)),
        )
      : [...this.rows()];

    const column = this.columns.get(this.sortKey() ?? '');
    if (!column) {
      // UNSORTED IS A REAL STATE and it is the one every table starts in: the
      // server already returns these in an order somebody chose.
      return found;
    }
    const dir = this.ascending() ? 1 : -1;
    // SORTING IN PLACE IS SAFE HERE and only here: found is a fresh array on
    // both paths above -- filter builds one, and the else branch spreads rather
    // than passing this.rows() through. Sorting the signal's own array would
    // reorder it under every other reader of it.
    //
    // sort rather than toSorted, which would say that in the method name: it
    // needs the ES2023 lib and this project targets ES2022, and moving the
    // whole target for one call is not a trade worth making.
    return found.sort((a, b) => compare(column.value(a), column.value(b), dir));
  });
}

/** A value as text, for searching. */
function text(v: string | number | null | undefined): string {
  return v === null || v === undefined ? '' : String(v);
}

/**
 * Order two cell values.
 *
 * BLANKS LAST IN BOTH DIRECTIONS, AND THE DIRECTION IS APPLIED IN HERE to make
 * that true. A station with no jock, an advert that never aired: sorting by
 * that column and getting a screenful of empty cells is not what anybody meant
 * by sorting, and reversing it must not make the empties the answer either. An
 * earlier version of this returned the blank verdict to a caller that
 * multiplied by the direction, which put every blank at the TOP on the second
 * click -- while the comment above it claimed the opposite.
 *
 * NUMBERS AS NUMBERS. Track counts and listener counts are numbers, and
 * comparing them as text puts 100 before 99 -- which is exactly the column an
 * operator sorts to find the biggest station.
 */
function compare(
  a: string | number | null | undefined,
  b: string | number | null | undefined,
  dir: number,
): number {
  const emptyA = a === null || a === undefined || a === '';
  const emptyB = b === null || b === undefined || b === '';
  if (emptyA || emptyB) {
    return emptyA && emptyB ? 0 : emptyA ? 1 : -1;
  }
  if (typeof a === 'number' && typeof b === 'number') {
    return (a - b) * dir;
  }
  return (
    String(a).localeCompare(String(b), undefined, { numeric: true, sensitivity: 'base' }) * dir
  );
}
