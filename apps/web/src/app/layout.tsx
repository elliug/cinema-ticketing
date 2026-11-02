import type { Metadata } from "next";
import type { ReactNode } from "react";
import { SiteFooter, SiteHeader } from "@/components";

import "./globals.css";

// The root layout is the product shell (M3.5 · T3 · S1): a header with the product
// name, the primary navigation and the way in to signing in, the page in the middle,
// and a footer that says what this build is.
//
// The pages themselves are untouched by it. Every surface in this application —
// the M0 health contract aside — renders `<main id="main-content">`, so Next's
// layout composition puts the existing seat, confirmation, ticket and administration
// screens inside the frame without a single edit to those files (S1).
//
// The description says what is true of every page of this build, and nothing about who
// is signed in: a session is optional, and metadata is not the place to guess at one
// (identity-login T6 · S7).
export const metadata: Metadata = {
  title: "Cinema Ticketing",
  description:
    "Public purchase and local administration for the cinema ticketing platform. Buying a ticket and opening one need no account; signing in is optional and only makes the list of your tickets something the box office answers.",
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
