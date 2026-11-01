// The administration's own shell (issue #92).
//
// It exists for one reason: four screens had two ways in, and two of them could only be
// reached by typing an address. A layout is the right place for the fix rather than four
// edits, because the promise being made is "every `/admin` screen is reachable from every
// other one" — a screen added later inherits it by existing, instead of having to remember
// to import a navigation.
//
// It deliberately renders no `<main>` and no `h1`: each screen owns its own `<main
// id="main-content">` and its own single first-level heading, and the browser gate asserts
// exactly one `h1` per screen. A shell that added either would break that count on all four
// at once.
import type { ReactNode } from "react";

import { AdminNav } from "./admin-nav";

export default function AdminLayout({ children }: { children: ReactNode }) {
  return (
    <>
      <AdminNav />
      {children}
    </>
  );
}
