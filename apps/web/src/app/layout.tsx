import type { Metadata } from "next";
import type { ReactNode } from "react";
import { SiteFooter, SiteHeader } from "@/components";
import { DEMO_SESSION_LABEL } from "@/lib/demo-session";

import "./globals.css";

// The root layout is the product shell (M3.5 · T3 · S1): a header with the product
// name, the primary navigation and the demonstration session, the page in the
// middle, and a footer that says what this build is.
//
// The pages themselves are untouched by it. Every surface in this application —
// the M0 health contract aside — renders `<main id="main-content">`, so Next's
// layout composition puts the existing seat, confirmation, ticket and administration
// screens inside the frame without a single edit to those files (S1).
//
// The description is built from the same exported constant the header renders, so
// the page metadata cannot describe the session differently from the interface
// (S2, S8).
export const metadata: Metadata = {
  title: "Cinema Ticketing",
  description: `Public purchase and local administration for the cinema ticketing platform. ${DEMO_SESSION_LABEL}`,
};

// No remote fonts or images are loaded: typography comes from the system font
// stack defined in globals.css, keeping the build and runtime self-contained.
export default function RootLayout({ children }: { children: ReactNode }) {
  return (
    <html lang="en">
      <body>
        {/* First focusable element of the document, so the keyboard can step over
            the header and land on the page itself (WCAG 2.4.1). Every page renders
            the `#main-content` target. */}
        <a className="skip-link" href="#main-content">
          Skip to content
        </a>
        <SiteHeader />
        {children}
        <SiteFooter />
      </body>
    </html>
  );
}
