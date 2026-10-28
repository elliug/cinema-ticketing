import type { Metadata } from "next";
import type { ReactNode } from "react";

import "./globals.css";

// No remote fonts or images are loaded: typography comes from the system font
// stack defined in globals.css, keeping the build and runtime self-contained.
export const metadata: Metadata = {
  title: "Cinema Ticketing — M0 Foundation",
  description:
    "M0 foundation is operational: the service and web application run locally so the repository baseline can be verified.",
};

export default function RootLayout({ children }: { children: ReactNode }) {
  return (
    <html lang="en">
      <body>{children}</body>
    </html>
  );
}
