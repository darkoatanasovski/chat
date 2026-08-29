"use client";

import { useState, type FormEvent } from "react";
import { useRouter } from "next/navigation";
import { LogIn } from "lucide-react";
import { login, ApiError } from "@/lib/api";
import { saveSession } from "@/lib/session";
import { Button, ErrorBanner, Input, Label, Panel } from "@/components/ui";

export default function LoginPage() {
  const router = useRouter();
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);

  async function handleSubmit(e: FormEvent) {
    e.preventDefault();
    setError(null);
    setLoading(true);
    try {
      const session = await login(email, password);
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
            <LogIn className="h-6 w-6" />
          </span>
          <h1 className="text-2xl font-semibold text-text">Welcome back</h1>
          <p className="text-[15px] text-text-muted">Sign in to manage your apps and API keys</p>
        </div>
        <Panel>
          <form onSubmit={handleSubmit} className="flex flex-col gap-4">
            <div>
              <Label>Email</Label>
              <Input type="email" required autoFocus value={email} onChange={(e) => setEmail(e.target.value)} placeholder="you@company.com" />
            </div>
            <div>
              <Label>Password</Label>
              <Input type="password" required value={password} onChange={(e) => setPassword(e.target.value)} placeholder="••••••••" />
            </div>
            <Button type="submit" variant="primary" loading={loading} className="mt-1 justify-center">
              Sign in
            </Button>
            {error && <ErrorBanner>{error}</ErrorBanner>}
          </form>
        </Panel>
        <p className="mt-6 text-center text-[15px] text-text-muted">
          Don&apos;t have an account?{" "}
          <a href="/signup" className="text-accent hover:underline">
            Create one
          </a>
        </p>
      </div>
    </div>
  );
}
