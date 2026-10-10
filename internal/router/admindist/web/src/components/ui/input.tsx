// Copyright 2026 Metrum AI
// SPDX-License-Identifier: Apache-2.0

import * as React from "react";
import { cn } from "@/lib/utils";

export const Input = React.forwardRef<HTMLInputElement, React.InputHTMLAttributes<HTMLInputElement>>(
  ({ className, ...props }, ref) => (
    <input
      ref={ref}
      className={cn(
        "h-9 w-full rounded-md border border-white/15 bg-black/35 px-3 py-1 text-sm text-white placeholder:text-white/45 focus-visible:outline-hidden focus-visible:ring-2 focus-visible:ring-metrum-blue",
        className,
      )}
      {...props}
    />
  ),
);
Input.displayName = "Input";
