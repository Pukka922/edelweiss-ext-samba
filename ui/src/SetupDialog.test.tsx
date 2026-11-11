import { cleanup, fireEvent } from "@solidjs/testing-library";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { Foreign } from "./api";
import { SetupDialog } from "./SetupDialog";
import { renderExt } from "./test/render";
import type { Failure } from "./useJob";

afterEach(cleanup);

function renderSetup(
  opts: {
    foreign?: Foreign;
    readOnly?: boolean;
    admin?: boolean;
    busy?: boolean;
    step?: string;
    failure?: Failure;
    locale?: "en" | "de";
  } = {},
) {
  const onStart = vi.fn();
  const onDismiss = vi.fn();
  const r = renderExt(
    () => (
      <SetupDialog
        foreign={opts.foreign}
        readOnly={opts.readOnly ?? false}
        admin={opts.admin ?? true}
        busy={opts.busy ?? false}
        step={opts.step}
        failure={opts.failure}
        onStart={onStart}
        onDismiss={onDismiss}
      />
    ),
    undefined,
    opts.locale ?? "de",
  );
  return { ...r, onStart, onDismiss };
}

describe("SetupDialog", () => {
  it("says what the setup does once, and starts it with its button", () => {
    const r = renderSetup();
    expect(r.getByRole("heading", { name: "Samba einrichten" })).toBeTruthy();
    expect(
      r.getByText(
        "Die Pakete sind da. Einmalig: Dienste in den Autostart nehmen und starten, Konfiguration schreiben.",
      ),
    ).toBeTruthy();
    expect(r.queryByRole("alert")).toBeNull();
    fireEvent.click(r.getByRole("button", { name: "Samba einrichten" }));
    expect(r.onStart).toHaveBeenCalledOnce();
  });

  it("names the shares and the users of a Samba that is there, and that both are kept with .edelweiss-bak", () => {
    const r = renderSetup({ foreign: { shares: ["public", "scans"], users: ["root", "scanner"] } });
    expect(r.getByRole("alert").textContent).toBe(
      "Es gibt schon Samba: /etc/samba/smb.conf mit den Freigaben „public“ und „scans“ und 2 Samba-Nutzer (root, scanner). Konfiguration und Nutzerdatenbank werden mit .edelweiss-bak gesichert; die Seite fängt leer an und übernimmt nichts.",
    );
  });

  it("words one share alone, and users alone, in German and in English", () => {
    const one = renderSetup({ foreign: { shares: ["public"], users: [] } });
    expect(one.getByRole("alert").textContent).toMatch(
      /^Es gibt schon Samba: \/etc\/samba\/smb\.conf mit der Freigabe „public“\. Konfiguration/,
    );
    cleanup();
    const users = renderSetup({ foreign: { shares: [], users: ["root"] }, locale: "en" });
    expect(users.getByRole("alert").textContent).toBe(
      "There is a Samba already: 1 Samba user (root). Its configuration and its user database are kept with .edelweiss-bak; the page starts empty and takes nothing over.",
    );
    cleanup();
    const both = renderSetup({ foreign: { shares: ["a", "b", "c"], users: ["x", "y"] }, locale: "en" });
    expect(both.getByRole("alert").textContent).toMatch(
      /^There is a Samba already: \/etc\/samba\/smb\.conf with the shares “a”, “b”, and “c” and 2 Samba users \(x, y\)\. /,
    );
  });

  it("says before admin mode that admin mode shows a Samba that is there", () => {
    const r = renderSetup({ admin: false });
    expect(
      r.getByText("Ob es schon ein Samba gibt, zeigt die Seite im Admin-Modus, bevor sie etwas ändert."),
    ).toBeTruthy();
    expect(r.getByRole("button", { name: "Samba einrichten" })).toBeTruthy();
  });

  it("shows the step while the setup runs, with the button disabled", () => {
    const r = renderSetup({ busy: true, step: "Schritt 1/4: Altes Samba sichern (.edelweiss-bak)" });
    expect(r.getByText("Schritt 1/4: Altes Samba sichern (.edelweiss-bak)")).toBeTruthy();
    expect((r.getByRole("button", { name: "Samba einrichten" }) as HTMLButtonElement).disabled).toBe(true);
  });

  it("shows the failure with the output of the failed command, until it is dismissed", () => {
    const r = renderSetup({
      failure: { text: "Schritt 2/4 ist fehlgeschlagen: x", output: "testparm: bad" },
    });
    expect(r.getByRole("alert").textContent).toContain("Schritt 2/4 ist fehlgeschlagen: x");
    expect(r.getByText("testparm: bad")).toBeTruthy();
    fireEvent.click(r.getByRole("button", { name: "Ausblenden" }));
    expect(r.onDismiss).toHaveBeenCalledOnce();
  });

  it("has no button for a user who can never get admin mode", () => {
    const r = renderSetup({ readOnly: true, admin: false });
    expect(r.queryByRole("button", { name: "Samba einrichten" })).toBeNull();
    expect(r.getByText("Samba einrichten kann nur ein Benutzer mit Admin-Modus.")).toBeTruthy();
  });
});
