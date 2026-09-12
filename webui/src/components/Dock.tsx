import { useState } from "react";
import { CopilotChat, CopilotKit } from "@copilotkit/react-core/v2";
import { GenUIHost } from "./GenUI";
import Library from "./Library";

const AGENT = "default";

type Tab = "library" | "chat";

// Dock is the secondary panel: it offers the session Library (dashboards and
// scripts you can launch onto the board) and, behind the "Ask AI" tab, the
// agent chat. The board — not the chat — is the primary surface of the UI.
export default function Dock({ token }: { token: string }) {
  const [tab, setTab] = useState<Tab>("library");

  return (
    <aside className="dock">
      <nav className="dock-tabs">
        <button
          className={tab === "library" ? "active" : ""}
          onClick={() => setTab("library")}
        >
          Library
        </button>
        <button
          className={tab === "chat" ? "active" : ""}
          onClick={() => setTab("chat")}
        >
          Ask AI
        </button>
      </nav>

      <div className={"dock-panel" + (tab === "library" ? "" : " hidden")}>
        <Library token={token} />
      </div>

      <div className={"dock-panel" + (tab === "chat" ? "" : " hidden")}>
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
    </aside>
  );
}