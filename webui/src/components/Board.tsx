import { useResults } from "../state/results";
import { ViewRender } from "./ViewRender";

// Board is the primary surface of the UI: an AI-assisted display of every
// task, dashboard and result produced during the session. Each notifications/
// view payload broadcast on this session's stream becomes a card on the grid;
// the chat is only one way to produce them (the Library tiles and the planner
// are the others).
export default function Board() {
  const { views, clear } = useResults();
  return (
    <section className="board">
      <div className="board-head">
        <strong>Workbench</strong>
        <span className="board-count">
          {views.length} {views.length === 1 ? "result" : "results"}
        </span>
        <button onClick={clear} title="Clear the workbench">
          clear
        </button>
      </div>
      <div className="board-grid">
        {views.length === 0 && (
          <div className="board-empty">
            <p>
              This session&rsquo;s display is empty — everything the agent
              produces (dashboards, task results, data tables) shows up here.
            </p>
            <p>
              Pick a dashboard in the <b>Library</b> or describe what you need
              in the <b>Ask AI</b> panel.
            </p>
          </div>
        )}
        {views.map((v, i) => (
          <ViewRender key={i} view={v} />
        ))}
      </div>
    </section>
  );
}