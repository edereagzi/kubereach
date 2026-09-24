import type { ReactNode } from "react";
import type { UseMutationResult } from "@tanstack/react-query";
import {
  AlertDialog,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Button } from "@/components/ui/button";
import { errorText } from "@/queries";

type ConfirmProps = {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: ReactNode;
  description: ReactNode;
  confirm: string;
  destructive?: boolean;
  disabled?: boolean;
  action: UseMutationResult<void, Error, void>;
  children?: ReactNode;
};

// ConfirmDialog stays open while its action runs and on failure, to say why.
export function ConfirmDialog({ open, onOpenChange, title, description, confirm, destructive, disabled, action, children }: ConfirmProps) {
  return (
    <AlertDialog open={open} onOpenChange={(o) => !action.isPending && onOpenChange(o)}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>{title}</AlertDialogTitle>
          <AlertDialogDescription>{description}</AlertDialogDescription>
        </AlertDialogHeader>
        {children}
        {action.error && <p className="text-xs text-destructive">{errorText(action.error)}</p>}
        <AlertDialogFooter>
          <AlertDialogCancel disabled={action.isPending}>Cancel</AlertDialogCancel>
          <Button variant={destructive ? "destructive" : "default"} disabled={disabled || action.isPending} onClick={() => action.mutate()}>
            {confirm}
          </Button>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}
