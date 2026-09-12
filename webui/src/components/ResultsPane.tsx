import { useResults } from "../state/results";
import { ViewRender } from "./ViewRender";

// ResultsPane shows every notifications/view payload broadcast on this
// session's stream: it is the landing spot for dashboards and run_task
// outputs, and doubles as a durable record even while the chat is mid-run.
export default function ResultsPane() {
  const { views, clear } = useResults();
  return (
    <section className="results">
      <div className="results-head">
        <strong>Results</strong>
        <button onClick={clear} title="Clear results">
          clear
        </button>
      </div>
      <div className="results-list">
        {views.length === 0 && (
          <div className="results-empty">
            Nothing yet. Ask the agent a question or run a view; outcomes appear
            here.
          </div>
        )}
        {views.map((v, i) => (
          <ViewRender key={i} view={v} />
        ))}
      </div>
    </section>
  );
}