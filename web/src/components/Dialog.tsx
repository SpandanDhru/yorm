import { useEffect, useRef, useState } from "react";

// ask shows a styled confirmation dialog, in place of the browser's
// confirm(), and resolves true if confirmed. DialogHost must be mounted.
export function ask(message: string, opts: { confirm?: string; danger?: boolean } = {}): Promise<boolean> {
  return new Promise((resolve) => {
    // A new question replaces one still open, which counts as cancelled.
    current?.resolve(false);
    current = { message, confirm: opts.confirm ?? "OK", danger: opts.danger ?? false, resolve };
    listeners.forEach((l) => l());
  });
}

interface Question {
  message: string;
  confirm: string;
  danger: boolean;
  resolve(ok: boolean): void;
}

let current: Question | null = null;
const listeners = new Set<() => void>();

// DialogHost renders whatever ask() is asking. Mount it once, at the root.
export function DialogHost() {
  const [q, setQ] = useState<Question | null>(current);
  const ref = useRef<HTMLDialogElement>(null);

  useEffect(() => {
    const update = () => setQ(current);
    listeners.add(update);
    return () => void listeners.delete(update);
  }, []);

  useEffect(() => {
    const d = ref.current;
    if (!d) return;
    if (q && !d.open) d.showModal(); // modal: blocks the page, traps focus, Esc cancels
    if (!q && d.open) d.close();
  }, [q]);

  const answer = (ok: boolean) => {
    const asked = current;
    current = null;
    setQ(null);
    asked?.resolve(ok);
  };

  return (
    <dialog
      ref={ref}
      className="dialog"
      onCancel={(e) => {
        e.preventDefault(); // Esc: close through answer, so the promise settles
        answer(false);
      }}
      onClick={(e) => e.target === ref.current && answer(false)} // a click on the backdrop
    >
      {q && (
        <form
          method="dialog"
          className="stack"
          onSubmit={(e) => {
            e.preventDefault();
            answer(true);
          }}
        >
          <p>{q.message}</p>
          <div className="row dialog-buttons">
            {/* For destructive questions, focus starts on Cancel, so a stray Enter doesn't delete anything. */}
            <button type="button" className="secondary" autoFocus={q.danger} onClick={() => answer(false)}>
              Cancel
            </button>
            <button className={q.danger ? "danger" : undefined} autoFocus={!q.danger}>
              {q.confirm}
            </button>
          </div>
        </form>
      )}
    </dialog>
  );
}
