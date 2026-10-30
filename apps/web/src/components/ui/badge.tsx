// shadcn/ui Badge (M3.5 · T3b). The word inside is the state; colour only reinforces it.
import { cva, type VariantProps } from "class-variance-authority";
import type { HTMLAttributes } from "react";

import { cn } from "@/lib/utils";

const badgeVariants = cva(
  "inline-flex items-center gap-1 rounded-full border px-2.5 py-0.5 text-xs font-medium whitespace-nowrap",
  {
    variants: {
      variant: {
        default: "border-transparent bg-primary text-primary-foreground",
        secondary: "border-transparent bg-secondary text-secondary-foreground",
        outline: "border-border text-foreground",
        success: "border-transparent bg-[var(--color-success-bg)] text-[var(--color-success-fg)]",
        warning: "border-transparent bg-[var(--color-warning-bg)] text-[var(--color-warning-fg)]",
        danger: "border-transparent bg-[var(--color-danger-bg)] text-[var(--color-danger-fg)]",
        info: "border-transparent bg-[var(--color-info-bg)] text-[var(--color-info-fg)]",
      },
    },
    defaultVariants: { variant: "default" },
  },
);

export interface BadgeProps
  extends HTMLAttributes<HTMLSpanElement>,
    VariantProps<typeof badgeVariants> {}

export function Badge({ className, variant, ...props }: BadgeProps) {
  return <span className={cn(badgeVariants({ variant }), className)} {...props} />;
}

export { badgeVariants };
