// The component set (M3.5 · T3 · S1).
//
// One import path for the shell's building blocks, so a screen never reaches into
// another screen's folder and so the inventory is one file long. Everything here is
// a server component except [SiteHeader], whose `"use client"` is the only one in
// this folder and is explained in its own file.
//
// What this milestone deliberately does not build is named in the task's handoff:
// no data table, no dialog, no toast, no date/time formatter. They belong to the
// screens that need them (T4–T8), where their rules can be tested with the data that
// justifies them.

export { Alert } from "./alert";
export type { AlertProps, AlertTone } from "./alert";

export { Badge, StatusPill } from "./badge";
export type { BadgeProps, StatusTone } from "./badge";

export { Button } from "./button";
export type { ButtonActionProps, ButtonLinkProps, ButtonProps, ButtonVariant } from "./button";

export { Card } from "./card";
export type { CardProps, CardTone } from "./card";

export { EmptyState } from "./empty-state";
export type { EmptyStateProps } from "./empty-state";

export { Field, FieldLabel, fieldIds } from "./field";
export type { FieldIds, FieldLabelProps, FieldProps } from "./field";

export { PageHeader } from "./page-header";
export type { PageHeaderProps } from "./page-header";

export { DemoSessionSwitcher, SITE_NAV, SITE_NAV_PENDING_LABEL, SiteHeader } from "./site-header";
export type { SiteNavItem } from "./site-header";

export { SiteFooter } from "./site-footer";

export { Skeleton } from "./skeleton";
export type { SkeletonProps } from "./skeleton";
