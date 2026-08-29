"use client";

import { useEffect } from "react";
import { useRouter } from "next/navigation";
import { loadSession } from "@/lib/session";

export default function Home() {
  const router = useRouter();
  useEffect(() => {
    router.replace(loadSession() ? "/overview" : "/login");
  }, [router]);
  return null;
}
