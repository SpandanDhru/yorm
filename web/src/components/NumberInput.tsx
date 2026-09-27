import { useEffect, useState, type InputHTMLAttributes, type KeyboardEvent } from "react";

type Props = Omit<InputHTMLAttributes<HTMLInputElement>, "value" | "onChange" | "type" | "min" | "max"> & {
  value: number;
  onChange(n: number): void;
  min: number;
  max: number;
};

// NumberInput is a box for whole numbers that you can type in freely:
// clear it, type a new number, even a minus sign first. It reports a value
// only when what's typed is a whole number, and on leaving the box puts
// back the last good value, kept within min and max. The arrow keys step.
export function NumberInput({ value, onChange, min, max, onBlur, onKeyDown, ...rest }: Props) {
  const [text, setText] = useState(String(value));

  // Follow changes from outside (a reset, a server update), but not the
  // echo of what's being typed.
  useEffect(() => {
    setText((t) => (Number(t) === value && t.trim() !== "" ? t : String(value)));
  }, [value]);

  const clamp = (n: number) => Math.max(min, Math.min(max, n));

  function change(t: string) {
    if (!/^-?\d*$/.test(t) || (t.startsWith("-") && min >= 0)) return; // ignore letters, and minus where it can't be
    setText(t);
    if (/^-?\d+$/.test(t)) onChange(Number(t));
  }

  function key(e: KeyboardEvent<HTMLInputElement>) {
    if (e.key === "ArrowUp" || e.key === "ArrowDown") {
      e.preventDefault();
      const n = clamp(value + (e.key === "ArrowUp" ? 1 : -1));
      setText(String(n));
      onChange(n);
    }
    onKeyDown?.(e);
  }

  return (
    <input
      {...rest}
      type="text"
      inputMode={min < 0 ? "text" : "numeric"} // phones' number pads have no minus sign
      autoComplete="off"
      value={text}
      onChange={(e) => change(e.target.value)}
      onKeyDown={key}
      onBlur={(e) => {
        const n = clamp(/^-?\d+$/.test(text) ? Number(text) : value);
        setText(String(n));
        if (n !== value) onChange(n);
        onBlur?.(e);
      }}
    />
  );
}
