import { cleanup, fireEvent } from "@solidjs/testing-library";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { SetupItem } from "./api";
import { ServiceAlert } from "./ServiceAlert";
import { items } from "./test/fixtures";
import { renderExt } from "./test/render";
import type { Failure } from "./useJob";

afterEach(cleanup);

function renderAlert(
  list: SetupItem[],
  opts: { readOnly?: boolean; busy?: boolean; step?: string; failure?: Failure; locale?: "en" | "de" } = {},
) {
  const onStart = vi.fn();
  const onDismiss = vi.fn();
  const r = renderExt(
    () => (
      <ServiceAlert
        items={list}
        readOnly={opts.readOnly ?? false}
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

describe("ServiceAlert", () => {
  it("is nothing while every service that the setup wants runs", () => {
    const r = renderAlert(items());
    expect(r.queryByRole("alert")).toBeNull();
    expect(r.queryByRole("status")).toBeNull();
    expect(r.queryByRole("button")).toBeNull();
  });

  it("names the stopped services that the setup wants running, and starts them with Starten", () => {
    const r = renderAlert(items({ samba: "not_started", crond: "not_enabled, not_started" }));
    expect(r.getByRole("alert").textContent).toContain(
      "Diese Dienste sind gestoppt: Samba und crond (Aufräumen der Papierkörbe).",
    );
    fireEvent.click(r.getByRole("button", { name: "Starten" }));
    expect(r.onStart).toHaveBeenCalledOnce();
  });

  it("names one stopped service in the singular, in English too", () => {
    const r = renderAlert(items({ avahi: "not_started, interfaces" }), { locale: "en" });
    expect(r.getByRole("alert").textContent).toContain("This service is stopped: Avahi (mDNS).");
    expect(r.getByRole("button", { name: "Start" })).toBeTruthy();
  });

  it("is no alert for a service that is not started on purpose, or that is open for another reason", () => {
    // with the discovery off, or without a local interface, the setup counts avahi and wsdd as they should be
    const r = renderAlert(items({ cleanup_script: "script", samba: "daemon_list", dbus: "not_enabled" }));
    expect(r.queryByRole("alert")).toBeNull();
    expect(r.queryByRole("button", { name: "Starten" })).toBeNull();
  });

  it("says that no chosen interface is local, without Starten", () => {
    const de = renderAlert(items({ discovery: "no_local_interface" }));
    expect(de.getByRole("status").textContent).toBe(
      "Keine gewählte Schnittstelle ist rein lokal (nur private IPv4-Adressen); Explorer und Finder finden den Server nicht.",
    );
    expect(de.queryByRole("button", { name: "Starten" })).toBeNull();
    cleanup();
    const en = renderAlert(items({ discovery: "no_local_interface" }), { locale: "en" });
    expect(en.getByRole("status").textContent).toBe(
      "No chosen interface is purely local (private IPv4 addresses only); Explorer and Finder do not find the server.",
    );
  });

  it("has no Starten for a user who can never get admin mode", () => {
    const r = renderAlert(items({ samba: "not_started" }), { readOnly: true });
    expect(r.getByRole("alert").textContent).toContain("Dieser Dienst ist gestoppt: Samba.");
    expect(r.queryByRole("button", { name: "Starten" })).toBeNull();
  });

  it("shows the step while the start runs, also when the services run by then, and its failure until dismissed", () => {
    const running = renderAlert(items(), {
      busy: true,
      step: "Schritt 1/2: Dienst starten oder stoppen (rc-service)",
    });
    expect(running.getByText("Schritt 1/2: Dienst starten oder stoppen (rc-service)")).toBeTruthy();
    cleanup();
    const failed = renderAlert(items({ samba: "not_started" }), { failure: { text: "rc-service: exit 1" } });
    expect(failed.getAllByRole("alert").some((a) => a.textContent === "rc-service: exit 1")).toBe(true);
    fireEvent.click(failed.getByRole("button", { name: "Ausblenden" }));
    expect(failed.onDismiss).toHaveBeenCalledOnce();
  });

  it("disables Starten while busy", () => {
    const r = renderAlert(items({ samba: "not_started" }), { busy: true });
    expect((r.getByRole("button", { name: "Starten" }) as HTMLButtonElement).disabled).toBe(true);
  });
});
