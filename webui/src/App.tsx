import { useState } from "react";
import Board from "./components/Board";
import Dock from "./components/Dock";
import Landing from "./components/Landing";
import { ResultsProvider } from "./state/results";
import { getSessionToken } from "./lib/session";

// App lays out the result-centric UI: the Workbench board is the stage where
// every session result (dashboards, tables, task outputs) is displayed, and
// the Dock hosts the session Library plus the "Ask AI" chat as an option — not
// as the primary surface.
export default function App() {
  const [token] = useState(() => getSessionToken());

  return (
    <div className="app">
      <Landing token={token} />
      <ResultsProvider token={token}>
        <div className="main">
          <Board />
          <Dock token={token} />
        </div>
      </ResultsProvider>
    </div>
  );
}