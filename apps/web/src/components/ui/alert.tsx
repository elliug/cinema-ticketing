// shadcn/ui Alert (M3.5 · T3b). The role is set by the caller's tone: an error is an
// interruption (role="alert"), an information message is not (role="status").
import { cva, type VariantProps } from "class-variance-authority";
import type { HTMLAttributes } from "react";

import { cn } from "@/lib/utils";

const alertVariants = cva("rounded-lg border-l-4 p-4 text-sm", {
  variants: {
    variant: {
      default: "border-border bg-card text-card-foreground",
      info: "border-[var(--color-info-fg)] bg-[var(--color-info-bg)] text-[var(--color-info-fg)]",
      success:
        "border-[var(--color-success-fg)] bg-[var(--color-success-bg)] text-[var(--color-success-fg)]",
      warning:
        "border-[var(--color-warning-fg)] bg-[var(--color-warning-bg)] text-[var(--color-warning-fg)]",
      destructive:
        "border-[var(--color-danger-fg)] bg-[var(--color-danger-bg)] text-[var(--color-danger-fg)]",
    },
  },
  defaultVariants: { variant: "default" },
});

export interface AlertProps
  extends HTMLAttributes<HTMLDivElement>,
    VariantProps<typeof alertVariants> {}

export function Alert({ className, variant, ...props }: AlertProps) {
  return <div className={cn(alertVariants({ variant }), className)} {...props} />;
}

export function AlertTitle({ className, ...props }: HTMLAttributes<HTMLParagraphElement>) {
  return <p className={cn("font-semibold", className)} {...props} />;
}

export function AlertDescription({ className, ...props }: HTMLAttributes<HTMLDivElement>) {
  return <div className={cn("mt-1", className)} {...props} />;
}
