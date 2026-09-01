"use client";

import { useEffect, useState } from "react";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { Eye, EyeOff } from "@/lib/icons";
import { useLogin, useMe, useVerifyTOTP } from "@/hooks/use-auth";
import { useForm } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { LoginSchema, type LoginInput } from "@repo/shared/schemas";
import { AuthShell } from "@/components/auth/AuthShell";

const inputBase =
  "w-full rounded-[var(--auth-radius)] border bg-[var(--auth-card)] px-4 py-3 text-[var(--auth-fg)] placeholder:text-[var(--auth-muted)] focus:outline-none focus:ring-2 transition-colors";
const inputOk = inputBase + " border-[var(--auth-border)] focus:border-[var(--auth-primary)] focus:ring-[var(--auth-primary)]/30";
const inputErr = inputBase + " border-red-400 focus:border-red-500 focus:ring-red-400/30";

export default function LoginPage() {
  const [showPassword, setShowPassword] = useState(false);
  // Holding a pending token means the password was right and 2FA is owed.
  const [pendingToken, setPendingToken] = useState<string | null>(null);
  const [code, setCode] = useState("");
  const [useBackup, setUseBackup] = useState(false);
  const [trustDevice, setTrustDevice] = useState(false);
  const { mutate: login, isPending, error: serverError } = useLogin();
  const { mutate: verifyTOTP, isPending: verifying, error: verifyError } = useVerifyTOTP();
  const { data: existingUser, isLoading: meLoading } = useMe();
  const router = useRouter();
  const { register, handleSubmit, formState: { errors } } = useForm<LoginInput>({
    resolver: zodResolver(LoginSchema),
  });

  // v3.31.15: if the session cookie is still valid, don't show the
  // login form — bounce straight to the dashboard.
  useEffect(() => {
    if (!meLoading && existingUser) {
      router.replace(existingUser.role === "USER" ? "/profile" : "/dashboard");
    }
  }, [meLoading, existingUser, router]);

  const onSubmit = (data: LoginInput) =>
    login(data, {
      onSuccess: (res) => {
        const d = res.data as { totp_required?: boolean; pending_token?: string };
        if (d?.totp_required && d.pending_token) setPendingToken(d.pending_token);
      },
    });

  const onVerify = (e: React.FormEvent) => {
    e.preventDefault();
    if (!pendingToken) return;
    verifyTOTP({
      pending_token: pendingToken,
      code,
      trust_device: trustDevice,
      backup: useBackup,
    });
  };

  const errText = (e: unknown) =>
    (e as { response?: { data?: { error?: { message?: string } } } })?.response?.data?.error?.message;
  const message = errText(serverError);
  const verifyMessage = errText(verifyError);

  // Step two: the password was accepted and the account has 2FA on. Rendered
  // instead of the credentials form rather than beneath it, so there is one
  // obvious thing to do.
  if (pendingToken) {
    return (
      <AuthShell
        mode="login"
        title="Two-factor authentication"
        subtitle={
          useBackup
            ? "Enter one of your backup codes"
            : "Enter the 6-digit code from your authenticator app"
        }
        errorMessage={verifyMessage}
      >
        <form onSubmit={onVerify} className="space-y-5">
          <div className="space-y-2">
            <label htmlFor="totp-code" className="block text-sm font-medium" style={{ color: "var(--auth-muted)" }}>
              {useBackup ? "Backup code" : "Authentication code"}
            </label>
            <input
              id="totp-code"
              value={code}
              onChange={(e) => setCode(e.target.value)}
              className={inputOk + " text-center tracking-[0.4em] text-lg"}
              placeholder={useBackup ? "XXXXXXXX" : "000000"}
              inputMode={useBackup ? "text" : "numeric"}
              autoComplete="one-time-code"
              maxLength={useBackup ? 8 : 6}
              autoFocus
            />
          </div>

          {!useBackup && (
            <label className="flex items-center gap-2 cursor-pointer text-sm" style={{ color: "var(--auth-muted)" }}>
              <input
                type="checkbox"
                checked={trustDevice}
                onChange={(e) => setTrustDevice(e.target.checked)}
                className="h-4 w-4 rounded border-[var(--auth-border)]"
              />
              Trust this device for 30 days
            </label>
          )}

          <button
            type="submit"
            disabled={verifying || code.trim().length < (useBackup ? 8 : 6)}
            className="w-full rounded-[var(--auth-radius)] py-3 font-medium text-white transition-opacity disabled:opacity-50"
            style={{ background: "var(--auth-primary)" }}
          >
            {verifying ? "Verifying…" : "Verify and sign in"}
          </button>

          <div className="flex items-center justify-between text-sm">
            <button
              type="button"
              onClick={() => { setUseBackup(!useBackup); setCode(""); }}
              style={{ color: "var(--auth-primary)" }}
            >
              {useBackup ? "Use your authenticator app" : "Use a backup code"}
            </button>
            <button
              type="button"
              onClick={() => { setPendingToken(null); setCode(""); setUseBackup(false); }}
              style={{ color: "var(--auth-muted)" }}
            >
              Back
            </button>
          </div>
        </form>
      </AuthShell>
    );
  }

  return (
    <AuthShell
      mode="login"
      title="Welcome back"
      subtitle="Sign in to your account"
      errorMessage={message}
    >
      <form onSubmit={handleSubmit(onSubmit)} className="space-y-5">
        <div className="space-y-2">
          <label htmlFor="email" className="block text-sm font-medium" style={{ color: "var(--auth-muted)" }}>
            Email
          </label>
          <input
            id="email"
            type="email"
            {...register("email")}
            className={errors.email ? inputErr : inputOk}
            placeholder="you@example.com"
            autoFocus
          />
          {errors.email && <p className="text-sm text-red-500">{errors.email.message}</p>}
        </div>

        <div className="space-y-2">
          <label htmlFor="password" className="block text-sm font-medium" style={{ color: "var(--auth-muted)" }}>
            Password
          </label>
          <div className="relative">
            <input
              id="password"
              type={showPassword ? "text" : "password"}
              {...register("password")}
              className={(errors.password ? inputErr : inputOk) + " pr-12"}
              placeholder="Enter your password"
            />
            <button
              type="button"
              onClick={() => setShowPassword(!showPassword)}
              className="absolute right-3 top-1/2 -translate-y-1/2"
              style={{ color: "var(--auth-muted)" }}
              aria-label={showPassword ? "Hide password" : "Show password"}
            >
              {showPassword ? <EyeOff className="h-5 w-5" /> : <Eye className="h-5 w-5" />}
            </button>
          </div>
          {errors.password && <p className="text-sm text-red-500">{errors.password.message}</p>}
        </div>

        <div className="flex items-center justify-between text-sm">
          <label className="flex items-center gap-2 cursor-pointer" style={{ color: "var(--auth-muted)" }}>
            <input type="checkbox" className="h-4 w-4 rounded border-[var(--auth-border)]" />
            Remember me
          </label>
          <Link href="/forgot-password" style={{ color: "var(--auth-primary)" }}>
            Forgot password?
          </Link>
        </div>

        <button
          type="submit"
          disabled={isPending}
          className="w-full rounded-[var(--auth-radius)] py-3 font-medium disabled:opacity-50 transition-colors"
          style={{ background: "var(--auth-primary)", color: "var(--auth-primary-fg)" }}
        >
          {isPending ? "Signing in..." : "Sign In"}
        </button>
      </form>
    </AuthShell>
  );
}
