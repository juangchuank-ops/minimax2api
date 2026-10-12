import * as React from "react";
import { createPortal } from "react-dom";

import { cn } from "@/shared/lib/cn";

type DropdownContextValue = {
  open: boolean;
  setOpen: (open: boolean) => void;
  anchor: React.RefObject<HTMLDivElement | null>;
  content: React.RefObject<HTMLDivElement | null>;
};

const DropdownContext = React.createContext<DropdownContextValue | null>(null);

function DropdownMenu({ children }: { children: React.ReactNode }) {
  const [open, setOpen] = React.useState(false);
  const anchor = React.useRef<HTMLDivElement | null>(null);
  const content = React.useRef<HTMLDivElement | null>(null);

  React.useEffect(() => {
    if (!open) return;
    // 内容 portal 到 body，不在 anchor 子树里，所以两个都要判。只判 anchor
    // 会把点菜单项当成点外部：菜单在 mousedown 与 mouseup 之间被卸载，
    // 两者不在同一元素上，click 根本不会产生，菜单项的 onClick 也就永不执行。
    function onPointerDown(event: MouseEvent): void {
      const target = event.target as Node;
      if (anchor.current?.contains(target) || content.current?.contains(target)) return;
      setOpen(false);
    }
    function onKeyDown(event: KeyboardEvent): void {
      if (event.key === "Escape") setOpen(false);
    }
    document.addEventListener("mousedown", onPointerDown);
    window.addEventListener("keydown", onKeyDown);
    return () => {
      document.removeEventListener("mousedown", onPointerDown);
      window.removeEventListener("keydown", onKeyDown);
    };
  }, [open]);

  return (
    <DropdownContext.Provider value={{ open, setOpen, anchor, content }}>
      <div ref={anchor} className="relative inline-flex">
        {children}
      </div>
    </DropdownContext.Provider>
  );
}

function DropdownMenuTrigger({
  children,
  asChild,
}: {
  children: React.ReactElement<{ onClick?: (event: React.MouseEvent) => void }>;
  asChild?: boolean;
}) {
  const context = React.useContext(DropdownContext);
  const trigger = asChild ? (
    children
  ) : (
    <button type="button">{children}</button>
  );
  return React.cloneElement(trigger, {
    onClick: (event: React.MouseEvent) => {
      event.stopPropagation();
      children.props.onClick?.(event);
      context?.setOpen(!context.open);
    },
  });
}

function DropdownMenuContent({
  className,
  align = "end",
  side = "bottom",
  children,
  ...props
}: React.ComponentProps<"div"> & { align?: "start" | "end"; side?: "top" | "bottom" }) {
  const context = React.useContext(DropdownContext);
  if (!context?.open) return null;
  return createPortal(
    <div
      ref={context.content}
      role="menu"
      className={cn(
        "fixed z-50 min-w-44 overflow-hidden rounded-md border border-border/60 bg-popover p-1 text-popover-foreground shadow-lg animate-in fade-in-0 zoom-in-95",
        className,
      )}
      style={dropdownPosition(context.anchor.current, align, side)}
      {...props}
    >
      {children}
    </div>,
    document.body,
  );
}

function dropdownPosition(
  anchor: HTMLElement | null,
  align: "start" | "end",
  side: "top" | "bottom",
): React.CSSProperties {
  if (!anchor) return { top: 0, left: 0 };
  const rect = anchor.getBoundingClientRect();
  return {
    top: side === "bottom" ? rect.bottom + 6 : undefined,
    bottom: side === "top" ? window.innerHeight - rect.top + 6 : undefined,
    left: align === "start" ? rect.left : undefined,
    right: align === "end" ? window.innerWidth - rect.right : undefined,
  };
}

function DropdownMenuItem({
  className,
  inset,
  onClick,
  ...props
}: React.ComponentProps<"button"> & { inset?: boolean }) {
  const context = React.useContext(DropdownContext);
  return (
    <button
      type="button"
      role="menuitem"
      onClick={(event) => {
        event.stopPropagation();
        onClick?.(event);
        context?.setOpen(false);
      }}
      className={cn(
        "flex h-8 w-full cursor-default select-none items-center gap-2 rounded-sm px-2 text-xs text-foreground outline-none transition-colors hover:bg-accent focus:bg-accent disabled:pointer-events-none disabled:opacity-50 [&_svg]:size-3.5 [&_svg]:shrink-0 [&_svg]:text-muted-foreground",
        inset && "pl-7",
        className,
      )}
      {...props}
    />
  );
}

function DropdownMenuSeparator({ className, ...props }: React.ComponentProps<"div">) {
  return <div role="separator" className={cn("-mx-1 my-1 h-px bg-border", className)} {...props} />;
}

function DropdownMenuLabel({ className, ...props }: React.ComponentProps<"div">) {
  return <div className={cn("px-2 py-1.5 text-[11px] text-muted-foreground", className)} {...props} />;
}

export {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
};
