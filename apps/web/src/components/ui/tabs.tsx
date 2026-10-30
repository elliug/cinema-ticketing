"use client";

// shadcn/ui Tabs (M3.5 · T3b): Radix provides roving focus and the aria relationships.
import * as TabsPrimitive from "@radix-ui/react-tabs";
import type { ComponentProps } from "react";

import { cn } from "@/lib/utils";

export const Tabs = TabsPrimitive.Root;

export function TabsList({ className, ...props }: ComponentProps<typeof TabsPrimitive.List>) {
  return (
    <TabsPrimitive.List
      className={cn("inline-flex items-center gap-1 rounded-md bg-muted p-1", className)}
      {...props}
    />
  );
}

export function TabsTrigger({ className, ...props }: ComponentProps<typeof TabsPrimitive.Trigger>) {
  return (
    <TabsPrimitive.Trigger
      className={cn(
        "rounded-sm px-3 py-1.5 text-sm font-medium text-muted-foreground",
        "focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none",
        "data-[state=active]:bg-card data-[state=active]:text-foreground",
        className,
      )}
      {...props}
    />
  );
}

export function TabsContent({ className, ...props }: ComponentProps<typeof TabsPrimitive.Content>) {
  return <TabsPrimitive.Content className={cn("mt-4 focus-visible:outline-none", className)} {...props} />;
}
