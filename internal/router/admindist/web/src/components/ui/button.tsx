// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

import * as React from "react";
import { cn } from "@/lib/utils";

export type ButtonProps = React.ButtonHTMLAttributes<HTMLButtonElement> & {
  variant?: "default" | "outline" | "ghost";
};

export const Button = React.forwardRef<HTMLButtonElement, ButtonProps>(
  ({ className, variant = "default", ...props }, ref) => (
    <button
      ref={ref}
      className={cn(
        "inline-flex h-9 items-center justify-center whitespace-nowrap rounded-md px-3 text-sm transition-colors focus-visible:outline-hidden focus-visible:ring-2 focus-visible:ring-metrum-blue disabled:pointer-events-none disabled:opacity-50",
        variant === "default" && "border border-metrum-purple bg-metrum-purple text-white hover:bg-metrum-magenta",
        variant === "outline" && "border border-white/15 bg-white/4 text-white hover:bg-white/8",
        variant === "ghost" && "text-white hover:bg-white/8",
        className,
      )}
      {...props}
    />
  ),
);
Button.displayName = "Button";
