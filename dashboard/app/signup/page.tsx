"use client";

import { useState, type FormEvent } from "react";
import { useRouter } from "next/navigation";
import { Sparkles } from "lucide-react";
import { signup, ApiError } from "@/lib/api";
import { saveSession } from "@/lib/session";
import { Button, ErrorBanner, Input, Label, Panel, Select } from "@/components/ui";

const TIERS = ["FREE", "PRO", "BUSINESS", "ENTERPRISE"] as const;

export default function SignupPage() {
  const router = useRouter();
  const [orgName, setOrgName] = useState("");
  const [tier, setTier] = useState<(typeof TIERS)[number]>("FREE");
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);

  async function handleSubmit(e: FormEvent) {
    e.preventDefault();
    setError(null);
    setLoading(true);
    try {
      const session = await signup(orgName, tier, email, password);
      saveSession(session);
      router.push("/overview");
    } catch (err) {
      setError(err instanceof ApiError ? err.message : String(err));
    } finally {
      setLoading(false);
    }
  }

  return (
    <div className="grid min-h-screen place-items-center px-4 py-10">
      <div className="w-full max-w-sm">
        <div className="mb-9 flex flex-col items-center gap-3.5 text-center">
          <span className="grid h-14 w-14 place-items-center rounded-full bg-accent-soft text-accent">
            <Sparkles className="h-6 w-6" />
          </span>
          <h1 className="text-2xl font-semibold text-text">Create your account</h1>
          <p className="text-[15px] text-text-muted">You&apos;ll be the owner of your new organization</p>
        </div>
        <Panel>
          <form onSubmit={handleSubmit} className="flex flex-col gap-4">
            <div>
              <Label>Organization name</Label>
              <Input required autoFocus value={orgName} onChange={(e) => setOrgName(e.target.value)} placeholder="Acme Inc" />
            </div>
            <div>
              <Label>Plan</Label>
              <Select value={tier} onChange={(e) => setTier(e.target.value as (typeof TIERS)[number])}>
                {TIERS.map((t) => (
                  <option key={t} value={t}>
                    {t}
                  </option>
                ))}
              </Select>
            </div>
            <div>
              <Label>Email</Label>
              <Input type="email" required value={email} onChange={(e) => setEmail(e.target.value)} placeholder="you@company.com" />
            </div>
            <div>
              <Label>Password</Label>
              <Input
                type="password"
                required
                minLength={8}
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                placeholder="At least 8 characters"
              />
            </div>
            <Button type="submit" variant="primary" loading={loading} className="mt-1 justify-center">
              Create account
            </Button>
            {error && <ErrorBanner>{error}</ErrorBanner>}
          </form>
        </Panel>
        <p className="mt-6 text-center text-[15px] text-text-muted">
          Already have an account?{" "}
          <a href="/login" className="text-accent hover:underline">
            Sign in
          </a>
        </p>
      </div>
    </div>
  );
}
