import { describe, expect, it } from 'vitest';
import { signal } from '@angular/core';
import { TableView } from './table-view';

// Jockora-9g7. Five admin tables get a search box and click-to-sort headers,
// and they share this rather than growing five copies of the same filtering.

interface Row {
  name: string;
  jock: string;
  tracks: number;
}

const rows: Row[] = [
  { name: 'ROCK', jock: 'Dutch', tracks: 412 },
  { name: 'ambient', jock: '', tracks: 99 },
  { name: 'Night Drive', jock: 'Sim', tracks: 100 },
];

function view(list: Row[] = rows) {
  const src = signal<Row[]>(list);
  return {
    src,
    v: new TableView<Row>(src, [
      { key: 'name', value: (r) => r.name },
      { key: 'jock', value: (r) => r.jock },
      { key: 'tracks', value: (r) => r.tracks },
    ]),
  };
}

const names = (v: TableView<Row>) => v.shown().map((r) => r.name);

describe('TableView', () => {
  it('starts in the order the server sent, which is a real state', () => {
    // Every table opens like this, and the rows already arrive in an order
    // somebody chose. Sorting on load would throw that away.
    const { v } = view();
    expect(names(v)).toEqual(['ROCK', 'ambient', 'Night Drive']);
    expect(v.sortKey()).toBeNull();
    expect(v.ariaSort('name')).toBe('none');
    expect(v.marker('name')).toBe('');
  });

  it('sorts by a column and turns it round on a second press', () => {
    const { v } = view();
    v.toggle('name');
    // CASE-INSENSITIVE, or "ambient" sorts after every capitalised name and an
    // operator reads it as unsorted.
    expect(names(v)).toEqual(['ambient', 'Night Drive', 'ROCK']);
    expect(v.ariaSort('name')).toBe('ascending');
    expect(v.marker('name')).toBe('▲');

    v.toggle('name');
    expect(names(v)).toEqual(['ROCK', 'Night Drive', 'ambient']);
    expect(v.ariaSort('name')).toBe('descending');
    expect(v.marker('name')).toBe('▼');
  });

  it('a third press does not drop back to unsorted', () => {
    // Two states are what a person expects from a header. An unsorted state
    // reachable by clicking is one nobody finds on purpose and everybody hits
    // by accident.
    const { v } = view();
    v.toggle('name');
    v.toggle('name');
    v.toggle('name');
    expect(v.sortKey()).toBe('name');
    expect(v.ascending()).toBe(true);
  });

  it('a different column starts ascending rather than inheriting the last one', () => {
    const { v } = view();
    v.toggle('name');
    v.toggle('name');
    expect(v.ascending()).toBe(false);
    v.toggle('tracks');
    expect(v.ascending()).toBe(true);
  });

  it('sorts numbers as numbers', () => {
    // Track counts and listener counts are numbers, and as text 100 sorts
    // before 99 -- in exactly the column an operator sorts to find the biggest
    // station.
    const { v } = view();
    v.toggle('tracks');
    expect(v.shown().map((r) => r.tracks)).toEqual([99, 100, 412]);
  });

  it('keeps blank cells at the bottom whichever way the column points', () => {
    // A station with no jock, an advert that never aired. Sorting by that
    // column and getting a screenful of empty cells is not what anybody meant,
    // and reversing it must not make the empties the answer either.
    const { v } = view();
    v.toggle('jock');
    expect(names(v)).toEqual(['Dutch', 'Sim', ''].map((j) => rows.find((r) => r.jock === j)!.name));
    expect(v.shown().at(-1)!.jock).toBe('');

    v.toggle('jock');
    expect(v.shown().at(-1)!.jock).toBe('');
  });

  it('treats a missing value as blank rather than the word null', () => {
    // A COLUMN CAN RETURN null: last_aired_at is absent on an advert that has
    // never played. Without this the search matches the string "null" and the
    // sort compares it as a word that lands between n and o.
    const src = signal([
      { name: 'never', jock: '', tracks: 1 },
      { name: 'also never', jock: '', tracks: 2 },
      { name: 'aired', jock: 'Dutch', tracks: 3 },
    ]);
    const v = new TableView<Row>(src, [
      { key: 'name', value: (r) => r.name },
      { key: 'aired', value: (r) => (r.jock ? r.jock : null) },
    ]);
    v.query.set('null');
    expect(v.shown()).toEqual([]);

    // AND TWO BLANKS ARE EQUAL, which keeps the sort stable instead of
    // shuffling the rows that have nothing in that column.
    v.query.set('');
    v.toggle('aired');
    expect(v.shown().map((r) => r.name)).toEqual(['aired', 'never', 'also never']);
  });

  it('searches every column, not just the first', () => {
    const { v } = view();
    v.query.set('dutch');
    expect(names(v)).toEqual(['ROCK']);
    v.query.set('412');
    expect(names(v)).toEqual(['ROCK']);
  });

  it('ignores case and surrounding space in the search', () => {
    const { v } = view();
    v.query.set('  NIGHT  ');
    expect(names(v)).toEqual(['Night Drive']);
  });

  it('searches and sorts together', () => {
    const { v } = view();
    v.query.set('i');
    v.toggle('name');
    expect(names(v)).toEqual(['ambient', 'Night Drive']);
  });

  it('never reorders the array the caller holds', () => {
    // The rows signal is shared: sorting it in place would reorder the table
    // for every other reader of it, including the one that wrote it.
    const { src, v } = view([...rows]);
    const before = [...src()];
    v.toggle('name');
    v.shown();
    expect(src()).toEqual(before);
  });

  it('ignores a sort key no column claims', () => {
    // Defensive rather than reachable from a header, and it is the branch that
    // decides whether an unknown key means unsorted or a crash.
    const { v } = view();
    v.sortKey.set('nonsense');
    expect(names(v)).toEqual(['ROCK', 'ambient', 'Night Drive']);
    expect(v.ariaSort('nonsense')).toBe('ascending');
  });

  it('follows the rows when they are reloaded', () => {
    const { src, v } = view();
    v.toggle('name');
    src.set([{ name: 'Zed', jock: 'Sim', tracks: 1 }]);
    expect(names(v)).toEqual(['Zed']);
  });
});
