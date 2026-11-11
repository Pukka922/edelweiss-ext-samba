const UNITS = ["B", "kB", "MB", "GB", "TB", "PB", "EB"];

/** A size in decimal units, as disks are labelled: "1.2 TB" ("1,2 TB" in German), "400 GB", "0 B". */
export function formatBytes(n: number, locale: string): string {
  let value = n;
  let unit = 0;
  while (unit < UNITS.length - 1 && value >= 1000) {
    value /= 1000;
    unit++;
  }
  // 999.96 GB is "1 TB", not "1,000 GB"
  if (unit > 0 && unit < UNITS.length - 1 && Math.round(value * 10) / 10 >= 1000) {
    value /= 1000;
    unit++;
  }
  const number = new Intl.NumberFormat(locale, { maximumFractionDigits: unit === 0 ? 0 : 1 }).format(value);
  return `${number} ${UNITS[unit]}`;
}

/**
 * How long ago the moment at (an ISO time) was, at now (milliseconds), in the words of the locale: "vor 5 Minuten", "vor 3
 * Stunden", "vor 2 Tagen". It says a minute at least, also for a moment that the clock of the browser has not reached.
 */
export function formatAgo(at: string, now: number, locale: string): string {
  const seconds = Math.max(0, (now - Date.parse(at)) / 1000);
  const words = new Intl.RelativeTimeFormat(locale, { numeric: "always" });
  if (seconds < 3600) return words.format(-Math.max(1, Math.floor(seconds / 60)), "minute");
  if (seconds < 86400) return words.format(-Math.floor(seconds / 3600), "hour");
  return words.format(-Math.floor(seconds / 86400), "day");
}

/** Names as a list in the words of the locale: "a und b" in German, "a, b, and c" in English. */
export const formatList = (names: string[], locale: string) =>
  new Intl.ListFormat(locale, { type: "conjunction" }).format(names);

/**
 * The name of the server in the network: the name of the settings, else the name that Samba takes then, the host name up
 * to its first dot ("" when the session has none).
 */
export const serverName = (name: string, hostname: string | undefined) =>
  name || (hostname ?? "").split(".")[0] || "";

/**
 * Whether version ("0.1.0", "v0.2.0-dev") is below min ([0, 2, 0]); an empty or unreadable version counts as below, since
 * nothing is known of it. A pre-release is taken as its release.
 */
export function versionBelow(version: string, min: [number, number, number]): boolean {
  const m = /^v?(\d+)\.(\d+)\.(\d+)/.exec(version);
  if (!m) return true;
  const got = [Number(m[1]), Number(m[2]), Number(m[3])];
  for (const [i, n] of min.entries()) {
    if ((got[i] as number) !== n) return (got[i] as number) < n;
  }
  return false;
}
