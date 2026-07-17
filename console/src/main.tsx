import { FluentProvider, webLightTheme } from "@fluentui/react-components";
import { createRoot } from "react-dom/client";
import { BrowserRouter } from "react-router-dom";
import { App } from "./shell";
import "./styles.css";

createRoot(document.getElementById("root")!).render(
  <FluentProvider theme={webLightTheme}>
    <BrowserRouter>
      <App />
    </BrowserRouter>
  </FluentProvider>
);
