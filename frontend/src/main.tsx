import { createRoot } from "react-dom/client";
import { setNonce } from "get-nonce";
import App from "./App";
import "./style.css";

const nonce = document.querySelector<HTMLMetaElement>(
  'meta[name="cpa-style-nonce"]',
)?.content;
if (nonce) setNonce(nonce);
const root = document.getElementById("root");
if (root) createRoot(root).render(<App />);
