import { StrictMode, useEffect, useState } from "react";
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
  if (page === "s" && id) return <Table sessionId={decodeURIComponent(id)} />;
  if (page === "join" && id) return <Join sessionId={decodeURIComponent(id)} />;
  return <Home />;
}

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <App />
  </StrictMode>,
);
