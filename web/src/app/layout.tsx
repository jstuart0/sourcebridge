import type { Metadata } from "next";
import { Inter } from "next/font/google";
import { Providers } from "./providers";
import "@/styles/globals.css";

const inter = Inter({ subsets: ["latin"], variable: "--font-inter" });

export const metadata: Metadata = {
  title: "SourceBridge.ai — Understand Any Codebase, Fast",
  description:
    "A codebase field guide and context layer for unfamiliar systems: explain code, review changes, see change impact, and connect specs to implementation when you need it.",
};

export default function RootLayout({ children }: { children: React.ReactNode }) {
  const edition = process.env.NEXT_PUBLIC_EDITION === "enterprise" ? "enterprise" : "oss";
  const defaultUiMode = edition === "enterprise" ? "control" : "editorial";

  return (
    <html
      lang="en"
      data-theme="dark"
      data-ui-mode={defaultUiMode}
      data-edition={edition}
      suppressHydrationWarning
    >
      <body className={`${inter.variable} font-[family-name:var(--font-inter)]`}>
        <Providers>{children}</Providers>
      </body>
    </html>
  );
}
