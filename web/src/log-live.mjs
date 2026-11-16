/**
 * @param {{onRefresh: () => Promise<void>, onStatus: (status: string) => void,
 * onUnauthorized: () => void, onError: () => void}} callbacks
 */
export function subscribeRequestLogs(callbacks) {
  if (typeof EventSource === "undefined") {
    callbacks.onStatus("unavailable");
    return () => {};
  }
  let stopped = false;
  let source;
  let timer;
  let reconnectTimer;
  let running = false;
  let dirty = false;
  const schedule = () => {
    dirty = true;
    if (stopped || running || timer) return;
    // A fixed batching window also refreshes under continuous traffic.
    timer = setTimeout(async () => {
      timer = undefined;
      if (stopped) return;
      dirty = false;
      running = true;
      try {
        await callbacks.onRefresh();
      } catch {
        callbacks.onError();
      } finally {
        running = false;
        if (dirty && !stopped) schedule();
      }
    }, 300);
  };
  const stop = () => {
    stopped = true;
    clearTimeout(timer);
    clearTimeout(reconnectTimer);
    source?.close();
  };
  const connect = () => {
    if (stopped) return;
    callbacks.onStatus("connecting");
    const current = new EventSource("/admin/api/logs/stream");
    source = current;
    current.addEventListener("connecting", () => {
      if (!stopped) callbacks.onStatus("connecting");
    });
    current.addEventListener("reconnecting", () => {
      if (!stopped) callbacks.onStatus("reconnecting");
    });
    for (const event of ["ready", "logs"]) {
      current.addEventListener(event, () => {
        if (stopped) return;
        callbacks.onStatus("live");
        schedule();
      });
    }
    current.addEventListener("unauthorized", () => {
      if (stopped) return;
      stop();
      callbacks.onUnauthorized();
    });
    current.onerror = () => {
      if (stopped) return;
      callbacks.onStatus("reconnecting");
      callbacks.onError();
      // Network failures reconnect natively. HTTP failures can close an
      // EventSource permanently, so retry those too after checking the session.
      if (current.readyState === 2) {
        current.close();
        clearTimeout(reconnectTimer);
        reconnectTimer = setTimeout(connect, 3000);
      }
    };
  };
  connect();
  return stop;
}
