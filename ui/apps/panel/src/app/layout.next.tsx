import type { Metadata } from "next";
import type { ReactNode } from "react";
import "@bedrock/design/tokens.css";
import "@bedrock/design/fonts.css";
import "../scloud-theme.css";

export const metadata: Metadata = {
  title: "sCloud Console",
  description: "Manage sCloud infrastructure with the Bedrock console.",
  robots: { index: false, follow: false },
};

export default function Layout({ children }: { children: ReactNode }) {
  return (
    <html lang="en" data-theme="dark">
      <body>{children}</body>
    </html>
  );
}
