import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import "@bedrock/design/tokens.css";
import "@bedrock/design/fonts.css";
import { App } from "./App";

const container = document.getElementById("root");
if (!container) {
  throw new Error("missing #root element");
}

createRoot(container).render(
  <StrictMode>
    <App />
  </StrictMode>,
);
