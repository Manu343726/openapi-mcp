import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useRef,
  useState,
  type ReactNode,
} from "react";
import type { ViewPayload } from "../lib/session";

// ResultsStore collects every notifications/view payload broadcast on this
// session's /ui/events stream. It is the data backing of the results pane and
// mirrors what the CopilotKit chat timeline shows, so run_task and view tool
// outcomes arrive here even when the chat thread is scrolled elsewhere.

interface ResultsStoreValue {
  views: ViewPayload[];
  append: (v: ViewPayload) => void;
  clear: () => void;
}

const ResultsContext = createContext<ResultsStoreValue | null>(null);

// useResults reads the live results pane store.
export function useResults(): ResultsStoreValue {
  const ctx = useContext(ResultsContext);
  if (!ctx) throw new Error("useResults requires <ResultsProvider>");
  return ctx;
}

const MAX_VIEWS = 100;

export function ResultsProvider({
  token,
  children,
}: {
  token: string;
  children: ReactNode;
}) {
  const [views, setViews] = useState<ViewPayload[]>([]);
  const latest = useRef<ViewPayload | null>(null);

  const append = useCallback((v: ViewPayload) => {
    latest.current = v;
    setViews((prev) => [...prev.slice(-(MAX_VIEWS - 1)), v]);
  }, []);

  const clear = useCallback(() => {
    latest.current = null;
    setViews([]);
  }, []);

  useEffect(() => {
    const es = new EventSource(`/ui/events?session=${encodeURIComponent(token)}`);
    es.onmessage = (ev) => {
      try {
        const msg = JSON.parse(ev.data as string) as {
          method?: string;
          params?: ViewPayload;
        };
        if (msg.method === "notifications/view" && msg.params) append(msg.params);
      } catch {
        // non-JSON or non-notification lines are ignored
      }
    };
    return () => es.close();
  }, [token, append]);

  return (
    <ResultsContext.Provider value={{ views, append, clear }}>
      {children}
    </ResultsContext.Provider>
  );
}