// shadcn/ui Input (M3.5 · T3b).
import type { InputHTMLAttributes } from "react";

import { cn } from "@/lib/utils";

export function Input({ className, type = "text", ...props }: InputHTMLAttributes<HTMLInputElement>) {
  return (
    <input
      type={type}
      className={cn(
        "h-10 w-full rounded-md border border-input bg-background px-3 py-2 text-sm",
        "outline-none placeholder:text-muted-foreground",
        "focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background",
        "aria-invalid:border-destructive aria-invalid:ring-destructive/30",
        "disabled:cursor-not-allowed disabled:opacity-50",
        className,
      )}
      {...props}
    />
  );
}
