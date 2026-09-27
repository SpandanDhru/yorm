import "@fontsource/press-start-2p";
import { StrictMode, useEffect, useState } from "react";
import { DialogHost } from "./components/Dialog";
import { createRoot } from "react-dom/client";
import { Home } from "./pages/Home";
import { Join } from "./pages/Join";
import { Table } from "./pages/Table";
import "./styles.css";

function App() {
  const [path, setPath] = useState(location.pathname);
  useEffect(() => {
    const onPop = () => setPath(location.pathname);
    addEventListener("popstate", onPop);
    return () => removeEventListener("popstate", onPop);
  }, []);

  const [, page, id] = path.split("/");
  return (
    <>
      {page === "s" && id ? (
        <Table sessionId={decodeURIComponent(id)} />
      ) : page === "join" && id ? (
        <Join sessionId={decodeURIComponent(id)} />
      ) : (
        <Home />
      )}
      <DialogHost />
    </>
  );
}

// The map canvas measures text when it draws, so let the pixel font load
// first (it's bundled, so this is quick; give up after a second anyway).
Promise.race([document.fonts.load('16px "Press Start 2P"'), new Promise((r) => setTimeout(r, 1000))]).finally(() =>
  createRoot(document.getElementById("root")!).render(
    <StrictMode>
      <App />
    </StrictMode>,
  ),
);
