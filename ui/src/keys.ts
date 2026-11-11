import { type Accessor, createMemo } from "solid-js";

/** Whether two lists of keys are the same keys in the same order. */
const sameKeys = (a: string[], b: string[]) => a.length === b.length && a.every((k, i) => k === b[i]);

/**
 * The keys of a list that a new view replaces every few seconds, for a For over the keys instead of over the items: a row
 * is by its key (the name of a share), not by position or by the object of the last view, so a new view keeps its element,
 * its buttons and their focus. keys changes only when the list of keys does; track(key), called in the row, is the item of
 * that key in the newest view, and once the item has left the view the last one the row had. The names of the shares are
 * unique: the server refuses a second one that differs only in case.
 */
export function keyed<T>(items: Accessor<T[]>, key: (item: T) => string) {
  const keys = createMemo<string[]>(() => items().map(key), [], { equals: sameKeys });
  const byKey = createMemo(() => new Map(items().map((item) => [key(item), item])));
  return {
    keys,
    track: (k: string) => createMemo<T>((last) => byKey().get(k) ?? (last as T)),
  };
}
