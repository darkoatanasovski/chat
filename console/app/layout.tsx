import type { Metadata } from "next";
import { GeistSans } from "geist/font/sans";
import { GeistMono } from "geist/font/mono";
import { QueryProvider } from "@/components/query-provider";
import { ToastProvider } from "@/components/toast";
import "./globals.css";

const SITE_URL = process.env.NEXT_PUBLIC_SITE_URL ?? "http://localhost:3001";

export const metadata: Metadata = {
  metadataBase: new URL(SITE_URL),
  title: {
    default: "Chat Platform Console",
    template: "%s · Chat Platform",
  },
  description: "Manage your organization's apps, API keys, team, and usage",
};

export default function RootLayout({ children }: { children: React.ReactNode }) {
  return (
    <html lang="en" className={`${GeistSans.variable} ${GeistMono.variable}`}>
      <body className="min-h-screen bg-bg text-text antialiased">
        <QueryProvider>
          <ToastProvider>{children}</ToastProvider>
        </QueryProvider>
      </body>
    </html>
  );
}
