import { describe, expect, it } from "vitest";
import { formatBytes, formatList, serverName, versionBelow } from "./format";

describe("formatBytes", () => {
  it("gives a size in decimal units, as disks are labelled, with the numbers of the language", () => {
    expect(formatBytes(0, "de")).toBe("0 B");
    expect(formatBytes(999, "en")).toBe("999 B");
    expect(formatBytes(4_100_000, "de")).toBe("4,1 MB");
    expect(formatBytes(1_200_000_000, "de")).toBe("1,2 GB");
    expect(formatBytes(1_200_000_000_000, "en")).toBe("1.2 TB");
  });

  it("never says 1000 of a unit: 999.96 GB is 1 TB", () => {
    expect(formatBytes(999_960_000_000, "en")).toBe("1 TB");
  });
});

describe("formatList", () => {
  it("joins names as the language does", () => {
    expect(formatList(["a"], "de")).toBe("a");
    expect(formatList(["a", "b"], "de")).toBe("a und b");
    expect(formatList(["a", "b", "c"], "en")).toBe("a, b, and c");
  });
});

describe("serverName", () => {
  it("is the name of the settings, else the host name up to its first dot", () => {
    expect(serverName("nas", "alpine.lan")).toBe("nas");
    expect(serverName("", "alpine.lan")).toBe("alpine");
    expect(serverName("", undefined)).toBe("");
  });
});

describe("versionBelow", () => {
  it("compares the numbers, and takes an unreadable version as below", () => {
    expect(versionBelow("0.1.0", [0, 2, 0])).toBe(true);
    expect(versionBelow("0.1.9", [0, 2, 0])).toBe(true);
    expect(versionBelow("0.2.0", [0, 2, 0])).toBe(false);
    expect(versionBelow("v0.2.0-dev", [0, 2, 0])).toBe(false);
    expect(versionBelow("0.10.0", [0, 2, 0])).toBe(false);
    expect(versionBelow("1.0.0", [0, 2, 0])).toBe(false);
    expect(versionBelow("", [0, 2, 0])).toBe(true);
    expect(versionBelow("dev", [0, 2, 0])).toBe(true);
  });
});
