import React from "react";
import ReactDOM from "react-dom/client";
import App from "./App";
import "./styles/tokens.css";
import "./styles/app.css";
import { installTooltip } from "./components/common/tooltip";

installTooltip();

// 初始主题，避免首帧闪烁；未手动切换过时默认深色
const saved = localStorage.getItem("wb-theme");
document.documentElement.dataset.theme =
  saved === "dark" || saved === "light" ? saved : "dark";

ReactDOM.createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <App />
  </React.StrictMode>,
);
