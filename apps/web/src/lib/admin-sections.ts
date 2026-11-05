// The administration's four screens, in the order an operator needs them (issue #92).
//
// The order is not alphabetical and not cosmetic: it is the order the API enforces. A
// screening cannot be scheduled without a film, a room and a *published* layout version,
// and a layout version cannot exist without a room, which needs a cinema with a time zone.
// Before this module existed, three of those four hops had no link at all, so the only way
// to follow the path was to know four URLs by heart — which is how "Create a film" came to
// look missing on a screen nobody could reach.
//
// The list lives here, rather than in the navigation component and again in the index page,
// because two copies of a path are a claim that can drift: the index would keep linking a
// screen the navigation had already renamed. Both render [ADMIN_SECTIONS].
//
// It lives in `lib` rather than beside the pages for two reasons: the repository keeps
// render-ready data and pure predicates here, and every module in this directory is free of
// the `@/` import alias so the `node --test` runner can load it without a bundler. The two
// path constants that already existed are imported relatively for the same reason.
import { ADMIN_SCHEDULE_PATH } from "./scheduling.ts";
import { ADMIN_VENUES_PATH } from "./venue-admin.ts";

// Re-exported so the four administration addresses can be read from one place: a caller that
// needs "where does the schedule screen live" should not have to know that this one constant
// historically belonged to the scheduling module and that one to the venue editor.
export { ADMIN_SCHEDULE_PATH, ADMIN_VENUES_PATH };

/** The layout editor's address. The editor predates a constant of its own. */
export const ADMIN_LAYOUTS_PATH = "/admin/layouts";

/** The door's address, written once so the section navigation cannot disagree with the header. */
export const ADMIN_DOOR_PATH = "/admin/door";

/** The index of the administration. It links to the four screens; it is not one of them. */
export const ADMIN_INDEX_PATH = "/admin";

/** One administration screen, as the section navigation and the index both describe it. */
export interface AdminSection {
  /** The address of the screen. */
  href: string;
  /** The name a person looks for, close enough to the screen's own `h1` to be honest. */
  label: string;
  /** One sentence: what this screen is for, in an operator's words rather than the schema's. */
  purpose: string;
  /**
   * Why this screen comes after the one before it. The index prints it; the navigation
   * does not, because a navigation is for moving and not for teaching.
   */
  prerequisite: string;
}

/**
 * The four screens, in working order.
 *
 * `purpose` and `prerequisite` are written for somebody setting a cinema up for the first
 * time, because that is the moment the order matters: a returning operator already knows it,
 * and a first-time operator is exactly who gets lost.
 *
 * Both are Castilian since the Marquesina redesign: the index, the section bar and the screens
 * themselves speak one language, and the section names are the four words the browser gate
 * looks for.
 */
export const ADMIN_SECTIONS: readonly AdminSection[] = [
  {
    href: ADMIN_VENUES_PATH,
    label: "Cines, salas y planos",
    purpose:
      "Crear un cine con su propia zona horaria, las salas que tiene dentro y sus versiones del plano.",
    prerequisite:
      "Nada va antes: todas las demás pantallas necesitan un cine y una sala a los que apuntar.",
  },
  {
    href: ADMIN_LAYOUTS_PATH,
    label: "Editor de planos",
    purpose:
      "Dibujar o corregir las filas y butacas de una sala, y publicar la versión que usarán las sesiones.",
    prerequisite:
      "Primero tiene que existir una sala, y una versión publicada es inmutable: corregirla es crear otra.",
  },
  {
    href: ADMIN_SCHEDULE_PATH,
    label: "Películas y sesiones",
    purpose:
      "Crear una película y atarla a una sala y a una versión publicada del plano, con un precio y un estado.",
    prerequisite:
      "La versión del plano de la sala ya tiene que estar publicada, y la película se crea aquí.",
  },
  {
    href: ADMIN_DOOR_PATH,
    label: "Acceso",
    purpose: "Admitir una entrada una sola vez y distinguir los cuatro veredictos que lee el personal.",
    prerequisite:
      "Sólo después de que una sesión haya vendido una entrada: el acceso admite lo que emitió el pago.",
  },
];

/**
 * Whether `pathname` is the given section's own screen.
 *
 * Exact match, deliberately. The administration's screens are four leaves, so a prefix match
 * would only introduce the trap this repository has already paid for once: `/admin` is a
 * prefix of `/administrator`, and a screen that does not belong to the section must never be
 * marked as if it did. The wider, section-level claim belongs to the header's primary
 * navigation, which declares the `/admin` prefix itself.
 *
 * A trailing slash is tolerated because a hand-typed address may carry one, and `/admin/` and
 * `/admin` are the same request to Next.
 */
export function isCurrentSection(pathname: string, href: string): boolean {
  if (pathname === "") {
    return false;
  }
  const normalized = pathname.length > 1 ? pathname.replace(/\/+$/, "") : pathname;
  return normalized === href;
}
