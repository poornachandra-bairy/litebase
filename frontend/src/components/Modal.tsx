import { useEffect, useRef } from "react";
import type { ReactNode } from "react";

interface ModalProps {
  title: string;
  onClose: () => void;
  children: ReactNode;
  footer?: ReactNode;
  wide?: boolean;
}

/** A dialog that closes on Escape and on a click outside its panel. */
export function Modal({ title, onClose, children, footer, wide }: ModalProps) {
  const panelRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
    };
    document.addEventListener("keydown", onKey);
    // The page behind must not scroll while a dialog is open.
    const prev = document.body.style.overflow;
    document.body.style.overflow = "hidden";
    return () => {
      document.removeEventListener("keydown", onKey);
      document.body.style.overflow = prev;
    };
  }, [onClose]);

  return (
    <div
      className="modal-backdrop"
      onMouseDown={(e) => {
        // Only a press that starts on the backdrop closes; this stops a drag
        // that begins inside the dialog from dismissing it.
        if (e.target === e.currentTarget) onClose();
      }}
    >
      <div className={`modal${wide ? " wide" : ""}`} ref={panelRef} role="dialog" aria-modal="true" aria-label={title}>
        <div className="modal-head">
          <h2>{title}</h2>
          <button className="btn ghost sm" onClick={onClose} aria-label="Close">×</button>
        </div>
        <div className="modal-body">{children}</div>
        {footer && <div className="modal-foot">{footer}</div>}
      </div>
    </div>
  );
}

interface ConfirmProps {
  title: string;
  message: ReactNode;
  confirmLabel?: string;
  danger?: boolean;
  busy?: boolean;
  onConfirm: () => void;
  onCancel: () => void;
}

/** A confirmation dialog for destructive actions. */
export function ConfirmDialog({
  title, message, confirmLabel = "Confirm", danger, busy, onConfirm, onCancel,
}: ConfirmProps) {
  return (
    <Modal
      title={title}
      onClose={onCancel}
      footer={
        <>
          <button className="btn" onClick={onCancel} disabled={busy}>Cancel</button>
          <button className={`btn ${danger ? "danger" : "primary"}`} onClick={onConfirm} disabled={busy}>
            {busy ? <span className="spinner" /> : confirmLabel}
          </button>
        </>
      }
    >
      <div className="small">{message}</div>
    </Modal>
  );
}
