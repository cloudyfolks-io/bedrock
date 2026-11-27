"use client";

import dynamic from "next/dynamic";

// Bedrock serves this shell for resource URLs that only exist at runtime.
// The panel owns those paths and talks to its authenticated same-origin proxy.
const Console = dynamic(() => import("../App").then((app) => app.App), {
  ssr: false,
  loading: () => (
    <main className="scloud-boot" aria-busy="true">
      <span>sCloud Console</span>
      <p>Loading your workspace…</p>
    </main>
  ),
});

export function Panel() {
  return <Console />;
}
