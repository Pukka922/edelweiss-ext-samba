import { cleanup, fireEvent, within } from "@solidjs/testing-library";
import { createSignal } from "solid-js";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { Right } from "../api";
import { users } from "../test/fixtures";
import { renderExt } from "../test/render";
import { RightsTable } from "./RightsTable";

afterEach(cleanup);

function renderTable(
  opts: {
    value?: Record<string, Right>;
    max?: Record<string, Right>;
    disabled?: boolean;
    usersKnown?: boolean;
    locale?: "en" | "de";
  } = {},
) {
  const [value, setValue] = createSignal<Record<string, Right>>(opts.value ?? {});
  const [list, setList] = createSignal(users());
  const onChange = vi.fn((v: Record<string, Right>) => setValue(v));
  const r = renderExt(
    () => (
      <RightsTable
        users={list()}
        value={value()}
        max={opts.max}
        disabled={opts.disabled}
        usersKnown={opts.usersKnown}
        onChange={onChange}
      />
    ),
    undefined,
    opts.locale ?? "de",
  );
  const group = (name: string) => r.getByRole("radiogroup", { name });
  const options = (name: string) =>
    within(group(name))
      .getAllByRole("radio")
      .map((o) => o.getAttribute("aria-label"));
  const checked = (name: string) =>
    within(group(name))
      .getAllByRole("radio")
      .find((o) => (o as HTMLInputElement).checked)
      ?.getAttribute("aria-label");
  const pick = (name: string, label: string) =>
    fireEvent.click(within(group(name)).getByRole("radio", { name: label }));
  return { ...r, onChange, value, setList, group, options, checked, pick };
}

describe("RightsTable", () => {
  it("lists every Samba user with its kind and the three rights, the value checked", () => {
    const r = renderTable({ value: { anna: "write", oma: "read" } });
    expect(r.getAllByRole("radiogroup").map((g) => g.getAttribute("aria-label"))).toEqual([
      "anna",
      "ben",
      "oma",
      "tv",
    ]);
    expect(r.options("anna")).toEqual(["kein Zugriff", "lesen", "lesen + schreiben"]);
    expect(r.checked("anna")).toBe("lesen + schreiben");
    expect(r.checked("ben")).toBe("kein Zugriff");
    expect(r.checked("oma")).toBe("lesen");
    const rows = r.getAllByRole("row");
    expect(rows[0]?.textContent).toContain("nur Samba");
    expect(rows[1]?.textContent).toContain("Host-Benutzer");
  });

  it("tells the new rights, without a user whose right is none", () => {
    const r = renderTable({ value: { anna: "write", oma: "read" } });
    r.pick("ben", "lesen");
    expect(r.onChange).toHaveBeenLastCalledWith({ anna: "write", oma: "read", ben: "read" });
    r.pick("anna", "kein Zugriff");
    expect(r.onChange).toHaveBeenLastCalledWith({ oma: "read", ben: "read" });
    expect(r.checked("anna")).toBe("kein Zugriff");
  });

  it("caps each row at max: a right above it is not offered, and a user without one is not listed", () => {
    const r = renderTable({ value: { anna: "write", ben: "write" }, max: { anna: "write", ben: "read" } });
    expect(r.getAllByRole("radiogroup").map((g) => g.getAttribute("aria-label"))).toEqual(["anna", "ben"]);
    expect(r.options("anna")).toEqual(["kein Zugriff", "lesen", "lesen + schreiben"]);
    expect(r.options("ben")).toEqual(["kein Zugriff", "lesen"]);
    // a value above the cap shows as the cap
    expect(r.checked("ben")).toBe("lesen");
    const rows = r.getAllByRole("row");
    expect(rows[0]?.textContent).toContain("Freigabe: ✎");
    expect(rows[1]?.textContent).toContain("Freigabe: 👁");
    expect(rows[0]?.textContent).not.toContain("nur Samba");
  });

  it("tells, with max, only what it shows: every right at most its cap, and no user without one in max", () => {
    const r = renderTable({
      value: { anna: "write", ben: "write", oma: "read", gone: "read" },
      max: { anna: "write", ben: "read" },
    });
    r.pick("anna", "lesen");
    expect(r.onChange).toHaveBeenLastCalledWith({ anna: "read", ben: "read" });
  });

  it("keeps its rows and a focused right when the users are replaced by equal ones", () => {
    const r = renderTable({ value: { oma: "read" } });
    const radio = within(r.group("oma")).getByRole("radio", { name: "lesen" }) as HTMLInputElement;
    const rows = r.getAllByRole("row");
    radio.focus();
    r.setList(users());
    expect(radio.isConnected).toBe(true);
    expect(document.activeElement).toBe(radio);
    expect(r.getAllByRole("row").every((tr, i) => tr === rows[i])).toBe(true);
  });

  it("follows a user whose kind changed in a new list", () => {
    const r = renderTable();
    const row = r.getAllByRole("row")[0] as HTMLElement;
    r.setList(users().map((u) => (u.name === "anna" ? { ...u, disabled: true } : u)));
    expect(r.getAllByRole("row")[0]).toBe(row);
    expect(row.textContent).toContain("deaktiviert");
  });

  it("is disabled while the change runs", () => {
    const r = renderTable({ disabled: true });
    expect(r.getAllByRole("radio").every((o) => (o as HTMLInputElement).disabled)).toBe(true);
  });

  it("marks a name of the rights that has no Samba entry as deleted, with no rights to give, when the users are known", () => {
    const r = renderTable({ value: { anna: "write", bob: "read", eve: "write" }, usersKnown: true });
    expect(r.getAllByRole("radiogroup").map((g) => g.getAttribute("aria-label"))).toEqual([
      "anna",
      "ben",
      "oma",
      "tv",
    ]);
    const deleted = r.getAllByRole("row").filter((tr) => tr.textContent?.includes("gelöscht"));
    expect(deleted.map((tr) => tr.querySelector("td")?.textContent)).toEqual(["bob", "eve"]);
    expect(deleted[0]?.textContent).toContain("kein Samba-Eintrag mehr; fällt beim Speichern weg");
    r.pick("ben", "lesen");
    expect(r.onChange).toHaveBeenLastCalledWith({ anna: "write", ben: "read" });
  });

  it("marks nothing as deleted while the users are not known", () => {
    const r = renderTable({ value: { anna: "write", bob: "read" } });
    expect(r.queryByText("gelöscht")).toBeNull();
  });

  it("offers no right to an account without a Samba entry, which is deleted where the rights name it", () => {
    const r = renderTable({ value: { lost: "read" }, usersKnown: true });
    r.setList([...users(), { name: "lost", kind: "samba", disabled: false, noEntry: true }]);
    expect(r.queryByRole("radiogroup", { name: "lost" })).toBeNull();
    expect(
      r
        .getAllByRole("row")
        .some((tr) => tr.textContent?.startsWith("lost") && tr.textContent.includes("gelöscht")),
    ).toBe(true);
  });

  it("puts the rights below the name on a narrow screen, so that nothing has to scroll sideways", () => {
    const r = renderTable({ value: { anna: "write" } });
    const row = r.group("anna").closest("tr") as HTMLElement;
    expect(row.className).toContain("flex-wrap");
    expect(row.className).toContain("sm:table-row");
    expect((r.group("anna").closest("td") as HTMLElement).className).toContain("basis-full");
  });

  it("speaks English", () => {
    const r = renderTable({ locale: "en" });
    expect(r.options("tv")).toEqual(["no access", "read", "read + write"]);
  });
});
