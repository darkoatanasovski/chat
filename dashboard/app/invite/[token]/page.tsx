"use client";

import { useState, type FormEvent } from "react";
import { useParams, useRouter } from "next/navigation";
import { UserPlus } from "lucide-react";
import { acceptInvite, ApiError } from "@/lib/api";
import { saveSession } from "@/lib/session";
import { Button, ErrorBanner, Input, Label, Panel } from "@/components/ui";

export default function AcceptInvitePage() {
  const router = useRouter();
  const params = useParams<{ token: string }>();
  const [password, setPassword] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);

  async function handleSubmit(e: FormEvent) {
    e.preventDefault();
    setError(null);
    setLoading(true);
    try {
      const session = await acceptInvite(params.token, password);
      saveSession(session);
      router.push("/overview");
    } catch (err) {
      setError(err instanceof ApiError ? err.message : String(err));
    } finally {
      setLoading(false);
    }
  }

  return (
    <div className="grid min-h-screen place-items-center px-4">
      <div className="w-full max-w-sm">
        <div className="mb-9 flex flex-col items-center gap-3.5 text-center">
          <span className="grid h-14 w-14 place-items-center rounded-full bg-accent-soft text-accent">
            <UserPlus className="h-6 w-6" />
          </span>
          <h1 className="text-2xl font-semibold text-text">You&apos;ve been invited</h1>
          <p className="text-[15px] text-text-muted">Set a password to join the team</p>
        </div>
        <Panel>
          <form onSubmit={handleSubmit} className="flex flex-col gap-4">
            <div>
              <Label>Password</Label>
              <Input
                type="password"
                required
                autoFocus
                minLength={8}
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                placeholder="At least 8 characters"
              />
            </div>
            <Button type="submit" variant="primary" loading={loading} className="mt-1 justify-center">
              Join team
            </Button>
            {error && <ErrorBanner>{error}</ErrorBanner>}
          </form>
        </Panel>
      </div>
    </div>
  );
}
