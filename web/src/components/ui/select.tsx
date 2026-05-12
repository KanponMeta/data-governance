import { clsx } from "clsx"
import { twMerge } from "tailwind-merge"

function cn(...inputs: (string | undefined | null | false)[]) {
  return twMerge(clsx(inputs))
}

type SelectTriggerProps = React.HTMLAttributes<HTMLDivElement>

export function SelectTrigger({ className, ...props }: SelectTriggerProps) {
  return (
    <div
      className={cn(
        "flex h-9 w-[200px] items-center justify-between rounded-md border border-input bg-transparent px-3 py-2 text-sm shadow-sm ring-offset-background placeholder:text-muted-foreground focus:outline-none focus:ring-1 focus:ring-ring disabled:cursor-not-allowed disabled:opacity-50",
        className
      )}
      {...props}
    />
  )
}

type SelectContentProps = React.HTMLAttributes<HTMLDivElement>

export function SelectContent({ className, ...props }: SelectContentProps) {
  return (
    <div
      className={cn(
        "relative z-50 min-w-[8rem] overflow-hidden rounded-md border bg-popover text-popover-foreground shadow-md data-[state=open]:animate-in data-[state=closed]:animate-out data-[state=closed]:fade-out-0 data-[state=open]:fade-in-0 data-[state=closed]:zoom-out-95 data-[state=open]:zoom-in-95 data-[side=bottom]:slide-in-from-top-2 data-[side=left]:slide-in-from-right-2 data-[side=right]:slide-in-from-left-2 data-[side=top]:slide-in-from-bottom-2",
        className
      )}
      {...props}
    />
  )
}

type SelectItemProps = React.HTMLAttributes<HTMLDivElement> & { value: string }

export function SelectItem({ className, value, ...props }: SelectItemProps) {
  return (
    <div
      className={cn(
        "relative flex w-full cursor-default select-none items-center rounded-sm py-1.5 pl-2 pr-8 text-sm outline-none focus:bg-accent focus:text-accent-foreground data-[disabled]:pointer-events-none data-[disabled]:opacity-50",
        className
      )}
      data-value={value}
      {...props}
    />
  )
}

type SelectValueProps = React.HTMLAttributes<HTMLSpanElement>

export function SelectValue({ className, ...props }: SelectValueProps) {
  return (
    <span className={cn("flex-1 truncate", className)} {...props} />
  )
}