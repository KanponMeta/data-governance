import * as React from "react"
import { clsx } from "clsx"
import { twMerge } from "tailwind-merge"

function cn(...inputs: (string | undefined | null | false)[]) {
  return twMerge(clsx(inputs))
}

interface SpinnerProps extends React.HTMLAttributes<HTMLDivElement> {
  size?: string
}

export function Spinner({ className, size = "h-6 w-6", ...props }: SpinnerProps) {
  return (
    <div
      className={cn("animate-spin border-2 border-primary border-t-transparent rounded-full", size, className)}
      {...props}
    />
  )
}