import { useId, useState, type InputHTMLAttributes, type ReactNode } from 'react';
import { Eye, EyeSlash } from '@phosphor-icons/react';
import { PASSWORD_MAX, PASSWORD_MIN, passwordLength, passwordLengthProblem } from '../api/auth';
import { cx } from '../ui/primitives';

/*
 * Form fields for signing in and choosing passwords (D183). Passwords stay in
 * component state only (never a URL, storage or a log); the browser's password
 * manager is helped with the autocomplete names.
 */

export const inputClass =
  'h-10 w-full rounded-[10px] bg-panel px-3 text-[14px] text-fg hairline placeholder:text-subtle focus:outline-2 focus:outline-offset-0 focus:outline-accent disabled:opacity-60 read-only:text-muted';

export function Field({ label, help, error, children, id }: { label: ReactNode; help?: ReactNode; error?: ReactNode; children: ReactNode; id: string }) {
  return (
    <div>
      <label htmlFor={id} className="mb-1.5 block text-[12.5px] font-medium text-muted">
        {label}
      </label>
      {children}
      {error ? (
        <p id={`${id}-help`} className="mt-1.5 text-[12px] text-danger" role="alert">
          {error}
        </p>
      ) : help ? (
        <p id={`${id}-help`} className="mt-1.5 text-[12px] text-subtle">
          {help}
        </p>
      ) : null}
    </div>
  );
}

export function TextField({ label, help, error, value, onChange, ...rest }: Omit<InputHTMLAttributes<HTMLInputElement>, 'onChange' | 'value'> & {
  label: ReactNode;
  help?: ReactNode;
  error?: ReactNode;
  value: string;
  onChange?: (v: string) => void;
}) {
  const id = useId();
  return (
    <Field id={id} label={label} help={help} error={error}>
      <input
        id={id}
        value={value}
        onChange={(e) => onChange?.(e.target.value)}
        aria-invalid={error ? true : undefined}
        aria-describedby={help || error ? `${id}-help` : undefined}
        className={inputClass}
        {...rest}
      />
    </Field>
  );
}

/** A password box with a show/hide toggle. */
export function PasswordField({ label, help, error, value, onChange, autoComplete, ...rest }: Omit<InputHTMLAttributes<HTMLInputElement>, 'onChange' | 'value' | 'type'> & {
  label: ReactNode;
  help?: ReactNode;
  error?: ReactNode;
  value: string;
  onChange: (v: string) => void;
  autoComplete: 'current-password' | 'new-password';
}) {
  const id = useId();
  const [shown, setShown] = useState(false);
  return (
    <Field id={id} label={label} help={help} error={error}>
      <div className="relative">
        <input
          id={id}
          type={shown ? 'text' : 'password'}
          value={value}
          onChange={(e) => onChange(e.target.value)}
          autoComplete={autoComplete}
          autoCapitalize="off"
          autoCorrect="off"
          spellCheck={false}
          aria-invalid={error ? true : undefined}
          aria-describedby={help || error ? `${id}-help` : undefined}
          className={cx(inputClass, 'pr-10')}
          {...rest}
        />
        <button
          type="button"
          onClick={() => setShown((v) => !v)}
          aria-label={shown ? 'Hide the password' : 'Show the password'}
          title={shown ? 'Hide the password' : 'Show the password'}
          className="absolute inset-y-0 right-0 grid w-10 place-items-center rounded-r-[10px] text-subtle hover:text-fg"
        >
          {shown ? <EyeSlash size={16} /> : <Eye size={16} />}
        </button>
      </div>
    </Field>
  );
}

/** Whether a new password and its "type it again" copy are good to send (the server checks the rest). */
export function newPasswordReady(pw: string, again: string): boolean {
  return !passwordLengthProblem(pw) && pw === again;
}

/**
 * A new password and "type it again": the length rule as you type, and a
 * match check once the second one is typed. The server refuses common
 * passwords and ones with your name or email in them; its words show in
 * `serverError`.
 */
export function NewPasswordFields({ password, again, onPassword, onAgain, serverError, label = 'Password', optional, disabled }: {
  password: string;
  again: string;
  onPassword: (v: string) => void;
  onAgain: (v: string) => void;
  serverError?: string | null;
  label?: string;
  /** Both may stay empty (keep the current password). */
  optional?: boolean;
  disabled?: boolean;
}) {
  const n = passwordLength(password);
  const lengthProblem = password ? passwordLengthProblem(password) : null;
  const mismatch = again.length > 0 && password !== again;
  const lengthHelp =
    optional && !password
      ? 'Leave both empty to keep your current password.'
      : n === 0
        ? `${PASSWORD_MIN} to ${PASSWORD_MAX} characters, any you like. A few words together works well.`
        : lengthProblem ?? `${n} characters: long enough.`;
  return (
    <>
      <PasswordField
        label={label}
        autoComplete="new-password"
        value={password}
        onChange={onPassword}
        maxLength={PASSWORD_MAX * 4}
        disabled={disabled}
        help={serverError ? undefined : lengthHelp}
        error={serverError || undefined}
        data-new-password
      />
      <PasswordField
        label="Type it again"
        autoComplete="new-password"
        value={again}
        onChange={onAgain}
        maxLength={PASSWORD_MAX * 4}
        disabled={disabled}
        error={mismatch ? "The two passwords don't match yet." : undefined}
        help={!mismatch && again && password === again ? 'They match.' : undefined}
        data-password-again
      />
    </>
  );
}
