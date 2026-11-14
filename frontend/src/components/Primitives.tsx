import { useEffect, useId, useRef, type ReactNode, type ButtonHTMLAttributes } from 'react';

export function Button({ primary = false, className = '', ...props }: ButtonHTMLAttributes<HTMLButtonElement> & { primary?: boolean }) {
  return <button {...props} className={`${primary ? 'primary' : ''} ${className}`} />;
}
export function Fieldset({ legend, hint, error, children }: { legend: string; hint?: string; error?: string; children: ReactNode }) {
  const id = useId();
  const ref = useRef<HTMLFieldSetElement>(null);
  useEffect(() => { if (error) ref.current?.querySelector<HTMLElement>('input,select,textarea,button')?.focus(); }, [error]);
  return <fieldset ref={ref} aria-describedby={`${hint ? `${id}-hint` : ''} ${error ? `${id}-error` : ''}`.trim() || undefined} aria-invalid={!!error}>
    <legend>{legend}</legend>{hint && <p id={`${id}-hint`} className="field-hint">{hint}</p>}{children}
    {error && <p id={`${id}-error`} className="field-error" role="alert">{error}</p>}
  </fieldset>;
}
export function Status({ children, tone = 'neutral' }: { children: ReactNode; tone?: 'neutral' | 'success' | 'warning' | 'error' }) {
  return <span className={`status status-${tone}`}>{children}</span>;
}
export function Panel({ title, children }: { title: string; children: ReactNode }) {
  return <section className="surface-panel" aria-label={title}><h2>{title}</h2>{children}</section>;
}
export function Dialog({ title, open, close, children }: { title: string; open: boolean; close: () => void; children: ReactNode }) {
  const ref = useRef<HTMLDialogElement>(null);
  const id = useId();
  useEffect(() => {
    const dialog = ref.current;
    if (!open || !dialog) return;
    const trigger = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    dialog.showModal();
    return () => { dialog.close(); trigger?.focus(); };
  }, [open]);
  return <dialog ref={ref} className="workspace-dialog" aria-labelledby={id} onCancel={(e) => { e.preventDefault(); close(); }}>
    <header><h2 id={id}>{title}</h2><Button onClick={close} aria-label={`Закрыть: ${title}`}>Закрыть</Button></header>{open ? children : null}
  </dialog>;
}
