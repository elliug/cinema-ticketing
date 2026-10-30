"use client";

// shadcn/ui Label (M3.5 · T3b): Radix, so clicking the label focuses the control.
import * as LabelPrimitive from "@radix-ui/react-label";
import type { ComponentProps } from "react";

import { cn } from "@/lib/utils";

export function Label({ className, ...props }: ComponentProps<typeof LabelPrimitive.Root>) {
  return (
    <LabelPrimitive.Root
      className={cn("text-sm font-medium leading-none", className)}
      {...props}
    />
  );
}
