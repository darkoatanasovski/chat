"use client";

import { useState, type FormEvent } from "react";
import { useRouter } from "next/navigation";
import { Sparkles } from "lucide-react";
import { signup, ApiError } from "@/lib/api";
import { saveSession } from "@/lib/session";
import { Button, ErrorBanner, Input, Label, Panel, WizardProgress } from "@/components/ui";

const WIZARD_LABELS = ["Organization", "First app"];

export default function SignupPage() {
  const router = useRouter();
  const [orgName, setOrgName] = useState("");
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);

  async function handleSubmit(e: FormEvent) {
    e.preventDefault();
    setError(null);
    setLoading(true);
    try {
      const session = await signup(orgName, email, password);
      saveSession(session);
      router.push("/console/overview");
    } catch (err) {
      setError(err instanceof ApiError ? err.message : String(err));
    } finally {
      setLoading(false);
    }
  }

  return (
    <div className="grid min-h-screen place-items-center px-4 py-10">
      <div className="w-full max-w-sm">
        <WizardProgress step={1} total={2} labels={WIZARD_LABELS} />
        <div className="mb-9 flex flex-col items-center gap-3.5 text-center">
          <span className="grid h-14 w-14 place-items-center rounded-full bg-accent-soft text-accent">
            <Sparkles className="h-6 w-6" />
          </span>
          <h1 className="text-2xl font-semibold text-text">Create your organization</h1>
          <p className="text-[15px] text-text-muted">You&apos;ll be the owner — invite your team once you&apos;re in.</p>
        </div>
        <Panel>
          <form onSubmit={handleSubmit} className="flex flex-col gap-4">
            <div>
              <Label>Organization name</Label>
              <Input required autoFocus value={orgName} onChange={(e) => setOrgName(e.target.value)} placeholder="Acme Inc" />
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
              Continue
            </Button>
            {error && <ErrorBanner>{error}</ErrorBanner>}
            <p className="text-center text-[13px] text-text-faint">Free to start. No credit card, no region to pick — upgrade anytime from Usage.</p>
          </form>
        </Panel>
        <p className="mt-6 text-center text-[15px] text-text-muted">
          Already have an account?{" "}
          <a href="/console/login" className="text-accent hover:underline">
            Sign in
          </a>
        </p>
      </div>
    </div>
  );
}
