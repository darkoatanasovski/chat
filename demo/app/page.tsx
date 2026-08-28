"use client";

import { useEffect, useState } from "react";
import { loadProfile } from "@/lib/session";
import type { Profile } from "@/lib/types";
import { SignIn } from "@/components/sign-in";
import { ChatApp } from "@/components/chat-app";

export default function HomePage() {
  const [profile, setProfile] = useState<Profile | null | undefined>(undefined);

  useEffect(() => {
    setProfile(loadProfile());
  }, []);

  if (profile === undefined) return null; // avoid a sign-in flash while localStorage loads

  if (!profile) {
    return (
      <div className="flex h-full items-center justify-center overflow-y-auto p-6">
        <SignIn onSignedIn={setProfile} />
      </div>
    );
  }

  return <ChatApp profile={profile} onProfileChange={setProfile} onSignedOut={() => setProfile(null)} />;
}
