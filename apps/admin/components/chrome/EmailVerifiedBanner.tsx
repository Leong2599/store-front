"use client";

import { useState } from "react";
import { useMutation } from "@tanstack/react-query";
import { useMe } from "@/hooks/use-auth";
import { apiClient } from "@/lib/api-client";
import { AlertTriangle, Check, Loader2 } from "@/lib/icons";

/**
 * Shown to a signed-in user whose address is still unconfirmed.
 *
 * Renders nothing at all when there is nothing to say — including while the
 * user query is still loading, so it never flashes in and out on every page
 * load for people who verified months ago.
 */
export function EmailVerifiedBanner() {
  const { data: user, isLoading } = useMe();
  const [sent, setSent] = useState(false);

  const resend = useMutation({
    mutationFn: async () => {
      await apiClient.post("/api/auth/verify-email/send", {});
    },
    onSuccess: () => setSent(true),
  });

  if (isLoading || !user || user.email_verified_at) return null;

  return (
    <div className="mx-6 mt-4 flex flex-wrap items-center gap-3 rounded-lg border border-warning/40 bg-warning/[0.07] px-4 py-3">
      <AlertTriangle className="h-4 w-4 shrink-0 text-warning" />
      <p className="min-w-0 flex-1 text-sm text-foreground">
        Confirm your email address.{" "}
        <span className="text-foreground-secondary">
          We sent a link to {user.email} when you signed up.
        </span>
      </p>

      {sent ? (
        <span className="inline-flex items-center gap-1.5 text-sm text-success">
          <Check className="h-3.5 w-3.5" />
          Sent — check your inbox
        </span>
      ) : (
        <button
          type="button"
          onClick={() => resend.mutate()}
          disabled={resend.isPending}
          className="inline-flex items-center gap-2 rounded-lg border border-border bg-bg-secondary px-3 py-1.5 text-sm hover:bg-bg-hover disabled:opacity-50"
        >
          {resend.isPending && <Loader2 className="h-3.5 w-3.5 animate-spin" />}
          Resend link
        </button>
      )}
    </div>
  );
}
