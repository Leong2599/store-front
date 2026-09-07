"use client";

import Link from "next/link";
import { AlertCircle, ArrowLeft, KeyRound, Monitor, ShieldCheck } from "lucide-react";
import { TwoFactorCard } from "@/components/profile/two-factor-card";
import { ActiveSessions } from "@/components/profile/active-sessions";
import { RecoveryContactCard } from "@/components/security/recovery-contacts";
import { PasskeysCard } from "@/components/security/passkeys";
import { useSecurityOverview } from "@/hooks/use-security";
import { SkeletonCards } from "@/components/ui/Skeleton";

/**
 * Everything that protects this account, on one screen.
 *
 * Deliberately not /system/security, which is the operator's threat dashboard:
 * blocked addresses, recent attacks, the health of the perimeter. That page is
 * about other people. This one is about you, and merging them would put a
 * "change your password" box next to a list of intrusion attempts.
 */
export default function AccountSecurityPage() {
  const { data: overview, isLoading } = useSecurityOverview();

  if (isLoading || !overview) {
    return (
      <div className="p-6">
        <SkeletonCards count={4} />
      </div>
    );
  }

  return (
    <div className="mx-auto max-w-3xl space-y-6 p-6">
      <div>
        <Link
          href="/profile"
          className="mb-2 inline-flex items-center gap-1.5 text-xs text-text-muted transition-colors hover:text-foreground"
        >
          <ArrowLeft className="h-3.5 w-3.5" aria-hidden="true" />
          Back to profile
        </Link>
        <h1 className="text-2xl font-bold tracking-tight text-foreground">Security</h1>
        <p className="mt-1 text-sm text-text-muted">
          How you sign in, and how you get back in if something goes wrong.
        </p>
      </div>

      {/* Ordered by what actually protects the account. Two-factor first
          because it is the single largest improvement available here, and an
          account without it is one leaked password away from gone. */}
      <TwoFactorCard />

      {/* Passkeys before recovery, because a passkey is the thing that makes
          the password matter less, and recovery is what you need when it does
          not. The card hides itself when the browser has no authenticator. */}
      <PasskeysCard />

      <RecoveryContactCard kind="email" overview={overview} />

      {/* Only when the deployment can actually send a text. A disabled control
          with no explanation is worse than no control. */}
      {overview.sms_provider_configured && (
        <RecoveryContactCard kind="phone" overview={overview} />
      )}

      {!overview.sms_provider_configured && (
        <div className="flex gap-3 rounded-xl border border-border bg-bg-secondary/50 p-4">
          <AlertCircle className="mt-0.5 h-4 w-4 shrink-0 text-text-muted" aria-hidden="true" />
          <p className="text-xs leading-relaxed text-text-muted">
            <span className="font-medium text-text-secondary">Phone recovery is not available.</span>{" "}
            This deployment has no SMS provider configured. Register one in{" "}
            <code className="rounded bg-bg-hover px-1 py-0.5 font-mono">internal/sms</code> and this
            option appears.
          </p>
        </div>
      )}

      {!overview.has_password && (
        <div className="flex gap-3 rounded-xl border border-warning/30 bg-warning/5 p-4">
          <KeyRound className="mt-0.5 h-4 w-4 shrink-0 text-warning" aria-hidden="true" />
          <p className="text-xs leading-relaxed text-text-secondary">
            This account signs in with {overview.provider}. Set a password on your profile before
            adding recovery contacts, since confirming them requires one.
          </p>
        </div>
      )}

      {/* ActiveSessions renders the list and nothing else: on the profile page
          it sits under that page's own heading. Given one here so it reads as
          a card like the rest of this screen rather than a loose list. */}
      <section className="rounded-xl border border-border bg-bg-elevated p-6">
        <div className="mb-4 flex items-start gap-3">
          <span className="mt-0.5 flex h-9 w-9 items-center justify-center rounded-lg bg-accent/10 text-accent">
            <Monitor className="h-4 w-4" aria-hidden="true" />
          </span>
          <div>
            <h2 className="text-sm font-semibold text-foreground">Active sessions</h2>
            <p className="mt-0.5 text-xs text-text-muted">
              Everywhere you are currently signed in. Sign out anything you do not recognise.
            </p>
          </div>
        </div>
        <ActiveSessions />
      </section>

      <div className="flex gap-3 rounded-xl border border-border bg-bg-secondary/50 p-4">
        <ShieldCheck className="mt-0.5 h-4 w-4 shrink-0 text-text-muted" aria-hidden="true" />
        <p className="text-xs leading-relaxed text-text-muted">
          Change your password on the{" "}
          <Link href="/profile" className="text-accent hover:underline">
            profile page
          </Link>
          . Changing it signs out every other device.
        </p>
      </div>
    </div>
  );
}
