// The class-name joiner shadcn components expect (M3.5 · T3b).
//
// `clsx` resolves conditional classes; `twMerge` then removes the earlier of two
// conflicting Tailwind utilities, so a component's default `px-4` can be overridden
// by a caller's `px-0` instead of both landing in the class list and letting source
// order decide. Nothing else belongs here: no formatting, no data logic.
import { clsx, type ClassValue } from "clsx";
import { twMerge } from "tailwind-merge";

export function cn(...inputs: ClassValue[]): string {
  return twMerge(clsx(inputs));
}
