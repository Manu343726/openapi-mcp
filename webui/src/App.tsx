import { useState } from "react";
import { CopilotChat, CopilotKit } from "@copilotkit/react-core/v2";
import { GenUIHost } from "./components/GenUI";
import Landing from "./components/Landing";
import ResultsPane from "./components/ResultsPane";
import { ResultsProvider } from "./state/results";
import { getSessionToken } from "./lib/session";

const AGENT = "default";

export default function App() {
  const [token] = useState(() => getSessionToken());

  return (
    <div className="app">
      <Landing token={token} />
      <ResultsProvider token={token}>
        <main className="main">
          <div className="chat">
            <CopilotKit
              agentId={AGENT}
              runtimeUrl="/ui/copilotkit"
              headers={{ "X-Ui-Session": token }}
            >
              <GenUIHost />
              <div className="chat-frame">
                <CopilotChat className="chat-root" />
              </div>
            </CopilotKit>
          </div>
          <ResultsPane />
        </main>
      </ResultsProvider>
    </div>
  );
}