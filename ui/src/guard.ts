import { useSession } from "@edelweiss/sdk";
import { createContext } from "solid-js";

/** What a change goes through: it opens (open) once admin mode is on. */
export type Guard = (open: () => void) => Promise<void>;

/**
 * The guard of the page, for the components in its dialogs that offer a change themselves (an upload): they
 * ask it for admin mode instead of reading the session. Without a page it opens at once.
 */
export const PageGuard = createContext<Guard>((open) => {
  open();
  return Promise.resolve();
});

/**
 * What comes before every change: act(open) asks for admin mode when it is off, and stops when it stays off. Every
 * change writes the Samba configuration and the accounts, which only root may do, and the process of the extension
 * runs as root in admin mode only: that is why a change needs admin mode, enforced by the system and not just by the page. As root the process also reads more than before (the markers of the jobs and the
 * system log can be for root only), so the view is read again once before anything opens: reload
 * resolves once the page has it, and rejects when it could not be read (the page shows the error, and nothing
 * opens). open runs last.
 */
export function useGuard(reload: () => Promise<unknown>): Guard {
  const session = useSession();
  const reloaded = () =>
    reload().then(
      () => true,
      () => false,
    );
  return async (open) => {
    if (!session.admin()) {
      if (!(await session.requestAdmin())) return;
      if (!(await reloaded())) return;
    }
    open();
  };
}
